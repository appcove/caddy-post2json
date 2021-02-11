// Copyright 2021 Matthew Holt
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package form2json

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	caddy.RegisterModule(Handler{})
}

// Handler parses multipart/form data and converts it to JSON.
type Handler struct {
	// The maximum bytes of memory to use when decoding form payloads.
	// Any files larger than this limit will be written to disk temporarily
	// while processing requests. Default: 2 MB
	MemoryLimit int64 `json:"memory_limit,omitempty"`
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.form2json",
		New: func() caddy.Module { return new(Handler) },
	}
}

// Provision sets up the module.
func (h Handler) Provision(_ caddy.Context) error {
	if h.MemoryLimit <= 0 {
		h.MemoryLimit = defaultMemLimit
	}
	return nil
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// passthru any requests we aren't equipped to handle (POST form data)
	if r.Method != http.MethodPost {
		return next.ServeHTTP(w, r)
	}
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/x-www-form-urlencoded") &&
		!strings.HasPrefix(ct, "multipart/form-data") {
		return next.ServeHTTP(w, r)
	}

	// read and parse the form payload, then close request body (we'll replace it later)
	err := r.ParseMultipartForm(h.MemoryLimit)
	if err != nil {
		return caddyhttp.Error(http.StatusBadRequest, err)
	}
	r.Body.Close()

	// assemble form data into structure for JSON
	var converted []part
	for name, values := range r.MultipartForm.Value {
		for _, v := range values {
			converted = append(converted, part{
				Name:  name,
				Type:  "field/text",
				Value: v,
			})
		}
	}
	for name, files := range r.MultipartForm.File {
		for _, file := range files {
			p, err := encodeFileIntoMemory(name, file)
			if err != nil {
				return caddyhttp.Error(http.StatusInternalServerError, err)
			}
			converted = append(converted, p)
		}
	}

	// delete temporary form data files
	if err := r.MultipartForm.RemoveAll(); err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	// prepare new request body buffer
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	// encode converted payload into our JSON buffer
	err = json.NewEncoder(buf).Encode(converted)
	if err != nil {
		return caddyhttp.Error(http.StatusInternalServerError, err)
	}

	// replace original request body with our buffer
	r.Body = ioutil.NopCloser(buf)

	// adjust request headers (and content length separately!)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Type-Class", "caddy_post_json_v1")
	r.Header.Set("Content-Length", strconv.Itoa(buf.Len()))
	r.ContentLength = int64(buf.Len())

	return next.ServeHTTP(w, r)
}

func encodeFileIntoMemory(name string, file *multipart.FileHeader) (part, error) {
	f, err := file.Open()
	if err != nil {
		return part{}, err
	}
	defer f.Close()

	buf := new(bytes.Buffer)
	b64enc := base64.NewEncoder(base64.StdEncoding, buf)
	_, err = io.Copy(b64enc, f)
	if err != nil {
		return part{}, err
	}
	b64enc.Close()

	return part{
		Name:        name,
		Type:        "file/base64",
		Value:       buf.String(),
		ContentType: file.Header.Get("Content-Type"),
		FileName:    file.Filename,
	}, nil
}

type part struct {
	Name        string `json:"name,omitempty"`
	Type        string `json:"type,omitempty"`
	Value       string `json:"value,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	FileName    string `json:"file_name,omitempty"`
}

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

const defaultMemLimit = 1024 * 1024 * 2

// Interface guards
var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
)

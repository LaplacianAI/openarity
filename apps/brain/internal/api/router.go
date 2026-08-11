package api

import (
	"net/http"
	"strings"
)

type route struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

type Router struct {
	prefix string
	routes []route
}

func NewRouter(prefix string) *Router {
	switch {
	case prefix == "":
		prefix = ""
	case !strings.HasPrefix(prefix, "/"):
		panic("api: router prefix must start with /: " + prefix)
	case strings.HasSuffix(prefix, "/"):
		panic("api: router prefix must not end with /: " + prefix)
	}
	return &Router{prefix: prefix}
}

func (r *Router) Get(pattern string, h http.HandlerFunc)    { r.handle(http.MethodGet, pattern, h) }
func (r *Router) Post(pattern string, h http.HandlerFunc)   { r.handle(http.MethodPost, pattern, h) }
func (r *Router) Put(pattern string, h http.HandlerFunc)    { r.handle(http.MethodPut, pattern, h) }
func (r *Router) Patch(pattern string, h http.HandlerFunc)  { r.handle(http.MethodPatch, pattern, h) }
func (r *Router) Delete(pattern string, h http.HandlerFunc) { r.handle(http.MethodDelete, pattern, h) }

func (r *Router) handle(method, pattern string, h http.HandlerFunc) {
	r.routes = append(r.routes, route{method: method, pattern: pattern, handler: h})
}

func (r *Router) Register(mux *http.ServeMux) {
	for _, rt := range r.routes {
		path := r.prefix + rt.pattern
		if path == "" {
			path = "/"
		}
		mux.HandleFunc(rt.method+" "+path, rt.handler)
	}
}

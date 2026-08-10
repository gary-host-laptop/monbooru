package web

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/monbooru/monbooru/internal/logx"
)

// pluginMountPrefix is where paired peers' own pages are served from.
const pluginMountPrefix = "/plugins/"

// pluginMountBase is the path a peer's pages answer under.
func pluginMountBase(name string) string { return pluginMountPrefix + name }

// pluginMount serves a paired peer's own pages under monbooru's address. An
// open-mode button cannot link at the peer directly: monbooru knows the
// address the peer offered at pairing, which is reachable from the server -
// a loopback port, a container name - and means nothing in a browser talking
// to monbooru through a reverse proxy.
//
// A peer's pages are therefore mounted, which puts them one prefix deep. The
// request carries that prefix so a page can build its own links; one built
// from the peer's root ("/crop/preview") would land on monbooru's routes
// instead, so relative links are what the guide asks authors for.
func (s *Server) pluginMount(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := s.plugin(name)
	base := s.pluginBase(p)
	if !ok || p.PeerToken == "" || base == "" {
		http.NotFound(w, r)
		return
	}
	target, err := url.Parse(base)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mount := pluginMountBase(name)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, mount)
			pr.Out.URL.RawPath = strings.TrimPrefix(pr.In.URL.EscapedPath(), mount)
			pr.SetURL(target)
			pr.SetXForwarded()
			// The peer checks this the way it checks a relay call: the
			// secret it minted at pairing is what says the call is ours.
			pr.Out.Header.Set("Authorization", "Bearer "+p.PeerToken)
			pr.Out.Header.Set("X-Monbooru-Plugin-Base", mount)
		},
		ModifyResponse: func(resp *http.Response) error {
			relocateToMount(resp, base, mount)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			s.markPluginDown(name)
			logx.Warnf("plugin %s: serving %s: %v", name, r.URL.Path, err)
			http.Error(w, "plugin "+name+" did not answer", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// relocateToMount keeps a peer's redirect inside the mount: its own absolute
// address is not reachable from the browser, and a root-relative one would
// land on monbooru's routes. A redirect to anywhere else - the {back_url} a
// plugin returns to when it is done - is left alone.
func relocateToMount(resp *http.Response, base, mount string) {
	loc := resp.Header.Get("Location")
	switch {
	case loc == "":
	case strings.HasPrefix(loc, base):
		resp.Header.Set("Location", mount+strings.TrimPrefix(loc, base))
	case strings.HasPrefix(loc, "/"):
		resp.Header.Set("Location", mount+loc)
	}
}

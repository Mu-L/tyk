package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	mathrand "math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	texttemplate "text/template"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TykTechnologies/graphql-go-tools/pkg/execution/datasource"
	"github.com/TykTechnologies/graphql-go-tools/pkg/graphql"
	"github.com/TykTechnologies/tyk-pump/analytics"

	"github.com/TykTechnologies/tyk/apidef"
	"github.com/TykTechnologies/tyk/config"
	"github.com/TykTechnologies/tyk/ctx"
	"github.com/TykTechnologies/tyk/dnscache"
	"github.com/TykTechnologies/tyk/header"
	"github.com/TykTechnologies/tyk/request"
	"github.com/TykTechnologies/tyk/test"
	"github.com/TykTechnologies/tyk/user"
)

func TestCopyHeader_NoDuplicateCORSHeaders(t *testing.T) {

	makeHeaders := func(withCORS bool) http.Header {

		var h = http.Header{}

		h.Set("Vary", "Origin")
		h.Set("Location", "https://tyk.io")

		if withCORS {
			for _, v := range corsHeaders {
				h.Set(v, "tyk.io")
			}
		}

		return h
	}

	tests := []struct {
		src, dst http.Header
	}{
		{makeHeaders(true), makeHeaders(false)},
		{makeHeaders(true), makeHeaders(true)},
		{makeHeaders(false), makeHeaders(true)},
	}

	for _, v := range tests {
		copyHeader(v.dst, v.src, false)

		for _, vv := range corsHeaders {
			val := v.dst[vv]
			if n := len(val); n != 1 {
				t.Fatalf("%s found %d times", vv, n)
			}

		}

	}
}

func TestReverseProxyRetainHost(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	target, _ := url.Parse("http://target-host.com/targetpath")
	cases := []struct {
		name          string
		inURL, inPath string
		retainHost    bool
		wantURL       string
	}{
		{
			"no-retain-same-path",
			"http://orig-host.com/origpath", "/origpath",
			false, "http://target-host.com/targetpath/origpath",
		},
		{
			"no-retain-minus-slash",
			"http://orig-host.com/origpath", "origpath",
			false, "http://target-host.com/targetpath/origpath",
		},
		{
			"retain-same-path",
			"http://orig-host.com/origpath", "/origpath",
			true, "http://orig-host.com/origpath",
		},
		{
			"retain-minus-slash",
			"http://orig-host.com/origpath", "origpath",
			true, "http://orig-host.com/origpath",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &APISpec{APIDefinition: &apidef.APIDefinition{}, URLRewriteEnabled: true}
			spec.URLRewriteEnabled = true

			req := TestReq(t, http.MethodGet, tc.inURL, nil)
			req.URL.Path = tc.inPath
			if tc.retainHost {
				setCtxValue(req, ctx.RetainHost, true)
			}

			proxy := ts.Gw.TykNewSingleHostReverseProxy(target, spec, nil)
			proxy.Director(req)

			if got := req.URL.String(); got != tc.wantURL {
				t.Fatalf("wanted url %q, got %q", tc.wantURL, got)
			}
		})
	}
}

type configTestReverseProxyDnsCache struct {
	*testing.T

	etcHostsMap map[string][]string
	dnsConfig   config.DnsCacheConfig
}

func (s *Test) flakySetupTestReverseProxyDnsCache(cfg *configTestReverseProxyDnsCache) func() {
	pullDomains := s.MockHandle.PushDomains(cfg.etcHostsMap, nil)
	s.Gw.dnsCacheManager.InitDNSCaching(
		time.Duration(cfg.dnsConfig.TTL)*time.Second, time.Duration(cfg.dnsConfig.CheckInterval)*time.Second)

	globalConf := s.Gw.GetConfig()
	enableWebSockets := globalConf.HttpServerOptions.EnableWebSockets

	globalConf.HttpServerOptions.EnableWebSockets = true
	s.Gw.SetConfig(globalConf)

	return func() {
		pullDomains()
		s.Gw.dnsCacheManager.DisposeCache()
		globalConf.HttpServerOptions.EnableWebSockets = enableWebSockets
		s.Gw.SetConfig(globalConf)
	}
}

func TestReverseProxyDnsCache(t *testing.T) {
	test.Flaky(t) // TODO: TT-5251

	const (
		host   = "orig-host.com."
		host2  = "orig-host2.com."
		host3  = "orig-host3.com."
		wsHost = "ws.orig-host.com."

		hostApiUrl       = "http://orig-host.com/origpath"
		host2HttpApiUrl  = "http://orig-host2.com/origpath"
		host2HttpsApiUrl = "https://orig-host2.com/origpath"
		host3ApiUrl      = "https://orig-host3.com/origpath"
		wsHostWsApiUrl   = "ws://ws.orig-host.com/connect"
		wsHostWssApiUrl  = "wss://ws.orig-host.com/connect"

		cacheTTL            = 5
		cacheUpdateInterval = 10
	)

	var (
		etcHostsMap = map[string][]string{
			host:   {"127.0.0.10", "127.0.0.20"},
			host2:  {"10.0.20.0", "10.0.20.1", "10.0.20.2"},
			host3:  {"10.0.20.15", "10.0.20.16"},
			wsHost: {"127.0.0.10", "127.0.0.10"},
		}
	)

	ts := StartTest(nil)
	ts.MockHandle, _ = test.InitDNSMock(etcHostsMap, nil)
	defer ts.Close()
	defer func() {
		_ = ts.MockHandle.ShutdownDnsMock()
	}()

	tearDown := ts.flakySetupTestReverseProxyDnsCache(&configTestReverseProxyDnsCache{t, etcHostsMap,
		config.DnsCacheConfig{
			Enabled: true, TTL: cacheTTL, CheckInterval: cacheUpdateInterval,
			MultipleIPsHandleStrategy: config.NoCacheStrategy}})

	currentStorage := ts.Gw.dnsCacheManager.CacheStorage()
	fakeDeleteStorage := &dnscache.MockStorage{
		MockFetchItem: currentStorage.FetchItem,
		MockGet:       currentStorage.Get,
		MockSet:       currentStorage.Set,
		MockDelete: func(key string) {
			//prevent deletion
		},
		MockClear: currentStorage.Clear}
	ts.Gw.dnsCacheManager.SetCacheStorage(fakeDeleteStorage)

	defer tearDown()

	cases := []struct {
		name string

		URL     string
		Method  string
		Body    []byte
		Headers http.Header

		isWebsocket bool

		expectedIPs    []string
		shouldBeCached bool
		isCacheEnabled bool
	}{
		{
			"Should cache first request to Host1",
			hostApiUrl,
			http.MethodGet, nil, nil,
			false,
			etcHostsMap[host],
			true, true,
		},
		{
			"Should cache first request to Host2",
			host2HttpsApiUrl,
			http.MethodPost, []byte("{ \"param\": \"value\" }"), nil,
			false,
			etcHostsMap[host2],
			true, true,
		},
		{
			"Should populate from cache second request to Host1",
			hostApiUrl,
			http.MethodGet, nil, nil,
			false,
			etcHostsMap[host],
			false, true,
		},
		{
			"Should populate from cache second request to Host2 with different protocol",
			host2HttpApiUrl,
			http.MethodPost, []byte("{ \"param\": \"value2\" }"), nil,
			false,
			etcHostsMap[host2],
			false, true,
		},
		{
			"Shouldn't cache request with different http verb to same host",
			hostApiUrl,
			http.MethodPatch, []byte("{ \"param2\": \"value3\" }"), nil,
			false,
			etcHostsMap[host],
			false, true,
		},
		{
			"Shouldn't cache dns record when cache is disabled",
			host3ApiUrl,
			http.MethodGet, nil, nil,
			false, etcHostsMap[host3],
			false, false,
		},
		{
			"Should cache ws protocol host dns records",
			wsHostWsApiUrl,
			http.MethodGet, nil,
			map[string][]string{
				"Upgrade":    {"websocket"},
				"Connection": {"Upgrade"},
			},
			true,
			etcHostsMap[wsHost],
			true, true,
		},
		// {
		// 	"Should cache wss protocol host dns records",
		// 	wsHostWssApiUrl,
		// 	http.MethodGet, nil,
		// 	map[string][]string{
		// 		"Upgrade":    {"websocket"},
		// 		"Connection": {"Upgrade"},
		// 	},
		// 	true,
		// 	etcHostsMap[wsHost],
		// 	true, true,
		// },
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storage := ts.Gw.dnsCacheManager.CacheStorage()
			if !tc.isCacheEnabled {
				ts.Gw.dnsCacheManager.SetCacheStorage(nil)
			}

			spec := &APISpec{APIDefinition: &apidef.APIDefinition{},
				EnforcedTimeoutEnabled: true,
				GlobalConfig:           config.Config{ProxyCloseConnections: true, ProxyDefaultTimeout: 0.1}}

			req := TestReq(t, tc.Method, tc.URL, tc.Body)
			for name, value := range tc.Headers {
				req.Header.Add(name, strings.Join(value, ";"))
			}

			Url, _ := url.Parse(tc.URL)
			proxy := ts.Gw.TykNewSingleHostReverseProxy(Url, spec, nil)
			recorder := httptest.NewRecorder()
			proxy.WrappedServeHTTP(recorder, req, false)

			host := Url.Hostname()
			if tc.isCacheEnabled {
				item, ok := storage.Get(host)
				if !ok || !test.IsDnsRecordsAddrsEqualsTo(item.Addrs, tc.expectedIPs) {
					t.Fatalf("got %q, but wanted %q. ok=%t", item, tc.expectedIPs, ok)
				}
			} else {
				item, ok := storage.Get(host)
				if ok {
					t.Fatalf("got %t, but wanted %t. item=%#v", ok, false, item)
				}
			}

			if !tc.isCacheEnabled {
				ts.Gw.dnsCacheManager.SetCacheStorage(storage)
			}
		})
	}
}

func (s *Test) TestNewWrappedServeHTTP() *ReverseProxy {

	target, _ := url.Parse(TestHttpGet)
	def := apidef.APIDefinition{}
	def.VersionData.DefaultVersion = "Default"
	def.VersionData.Versions = map[string]apidef.VersionInfo{
		"Default": {
			Name:             "v2",
			UseExtendedPaths: true,
			ExtendedPaths: apidef.ExtendedPathsSet{
				TransformHeader: []apidef.HeaderInjectionMeta{
					{
						DeleteHeaders: []string{"header"},
						AddHeaders:    map[string]string{"newheader": "newvalue"},
						Path:          "/abc",
						Method:        "GET",
						ActOnResponse: true,
					},
				},
				URLRewrite: []apidef.URLRewriteMeta{
					{
						Path:         "/get",
						Method:       "GET",
						MatchPattern: "/get",
						RewriteTo:    "/post",
					},
				},
			},
		},
	}
	spec := &APISpec{
		APIDefinition:          &def,
		EnforcedTimeoutEnabled: true,
		CircuitBreakerEnabled:  true,
	}
	return s.Gw.TykNewSingleHostReverseProxy(target, spec, nil)
}

func createReverseProxyAndServeHTTP(ts *Test, req *http.Request) (*httptest.ResponseRecorder, ProxyResponse) {
	proxy := ts.TestNewWrappedServeHTTP()
	recorder := httptest.NewRecorder()
	resp := proxy.WrappedServeHTTP(recorder, req, false)

	return recorder, resp
}

func TestWrappedServeHTTP(t *testing.T) {
	idleConnTimeout = 1

	ts := StartTest(nil)
	defer ts.Close()

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		_, _ = createReverseProxyAndServeHTTP(ts, req)
	}

	assert.Equal(t, 10, ts.Gw.ConnectionWatcher.Count())
	time.Sleep(time.Second * 2)
	assert.Equal(t, 0, ts.Gw.ConnectionWatcher.Count())

	// Test error on deepCopyBody function
	mockReadCloser := createMockReadCloserWithError(errors.New("test error"))
	req := httptest.NewRequest(http.MethodPost, "/test", mockReadCloser)
	// Set any ContentLength - httptest.NewRequest sets it only for bytes.Buffer, bytes.Reader and strings.Reader
	req.ContentLength = 1
	recorder, proxyResponse := createReverseProxyAndServeHTTP(ts, req)
	assert.NotNil(t, proxyResponse, "error on deepCopyBody should return an empty ProxyResponse")
	assert.Nil(t, proxyResponse.Response, "no response should be expected on error")
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func TestCircuitBreaker5xxs(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	t.Run("Extended Paths", func(t *testing.T) {
		ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
			UpdateAPIVersion(spec, "v1", func(v *apidef.VersionInfo) {
				json.Unmarshal([]byte(`[
					{
						"path": "error",
						"method": "GET",
						"threshold_percent": 0.1,
						"samples": 3,
						"return_to_service_after": 6000
					}
  			 	]`), &v.ExtendedPaths.CircuitBreaker)
			})
			spec.Proxy.ListenPath = "/"
			spec.CircuitBreakerEnabled = true
		})

		ts.Run(t, []test.TestCase{
			{Path: "/errors/500", Code: http.StatusInternalServerError},
			{Path: "/errors/501", Code: http.StatusNotImplemented},
			{Path: "/errors/502", Code: http.StatusBadGateway},
			{Path: "/errors/500", Code: http.StatusServiceUnavailable},
			{Path: "/errors/501", Code: http.StatusServiceUnavailable},
			{Path: "/errors/502", Code: http.StatusServiceUnavailable},
		}...)
	})
}

func TestCircuitBreakerEvents(t *testing.T) {
	// Use this channel to capture webhook events:
	triggeredEvent := make(chan apidef.TykEvent)

	// Establish a simple HTTP server that takes webhook input and passes the event to above channel:
	webHookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}

		// Unmarshal webhook input, we're only interested in event's name:
		var eventData map[string]interface{}
		if err := json.Unmarshal(rawBody, &eventData); err != nil {
			t.Fatal(err)
		}
		eventName, ok := eventData["event"].(string)
		if !ok {
			t.Fatal("invalid webhook input")
		}
		triggeredEvent <- apidef.TykEvent(eventName)
	}))

	// Establish another HTTP server to trigger CB behavior
	// Uses a counter to send an error response on the 1st sample:
	var sampleCount int
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sampleCount == 1 {
			w.WriteHeader(500)
			sampleCount++
			return
		}
		w.WriteHeader(200)
		sampleCount++
	}))

	ts := StartTest(nil)
	defer ts.Close()

	// Events to capture on this API, we use default webhook template:
	events := map[apidef.TykEvent][]apidef.EventHandlerTriggerConfig{
		EventBreakerTripped: {
			{
				Handler: EH_WebHook,
				HandlerMeta: map[string]interface{}{
					"method":        http.MethodPost,
					"target_path":   webHookServer.URL,
					"template_path": "templates/default_webhook.json",
					"event_timeout": 10,
				},
			},
		},
		EventBreakerReset: {
			{
				Handler: EH_WebHook,
				HandlerMeta: map[string]interface{}{
					"method":        http.MethodPost,
					"target_path":   webHookServer.URL,
					"template_path": "templates/default_webhook.json",
					"event_timeout": 10,
				},
			},
		},
	}

	// Setup an API definition with CB settings and attach above event handlers:
	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.Proxy.TargetURL = upstreamServer.URL
		spec.Proxy.ListenPath = "/circuitbreaker/"
		spec.CircuitBreakerEnabled = true
		UpdateAPIVersion(spec, "v1", func(v *apidef.VersionInfo) {
			err := json.Unmarshal([]byte(`[
					{
						"path": "test",
						"method": "GET",
						"threshold_percent": 0.1,
						"samples": 1,
						"return_to_service_after": 1
					}
				   ]`), &v.ExtendedPaths.CircuitBreaker)
			if err != nil {
				t.Fatal(err)
			}
		})
		spec.EventHandlers.Events = events
	})

	// Run the first series of requests, 1st sample should trigger the CB:
	_, err := ts.Run(t, []test.TestCase{
		{Path: "/circuitbreaker/test", Code: http.StatusOK},
		{Path: "/circuitbreaker/test", Code: http.StatusInternalServerError},
	}...)
	if err != nil {
		t.Fatal(err)
	}

	// Validate if the first event is the expected one:
	e := <-triggeredEvent
	if e != EventBreakerTripped {
		t.Fatalf("invalid event, got '%s', expecting '%s'", e, EventBreakerTripped)
	}

	// Run the third request which should already be an HTTP 503 from CB:
	_, err = ts.Run(t, []test.TestCase{
		{Path: "/circuitbreaker/test", Code: http.StatusServiceUnavailable},
	}...)
	if err != nil {
		t.Fatal(err)
	}

	// Wait as long as "return_to_service_after" specifies before retrying again
	// This request will be an HTTP 200:
	time.Sleep(1000 * time.Millisecond)

	_, err = ts.Run(t, []test.TestCase{
		{Path: "/circuitbreaker/test", Code: http.StatusOK},
	}...)
	if err != nil {
		t.Fatal(err)
	}

	// Validate if the last emitted event is a breaker reset:
	e = <-triggeredEvent
	if e != EventBreakerReset {
		t.Fatalf("invalid event, got '%s', expecting '%s'", e, EventBreakerReset)
	}
}

func TestSingleJoiningSlash(t *testing.T) {
	testsFalse := []struct {
		a, b, want string
	}{
		{"", "", ""},
		{"/", "", ""},
		{"", "/", ""},
		{"/", "/", ""},
		{"foo", "", "foo"},
		{"foo", "/", "foo"},
		{"foo", "bar", "foo/bar"},
		{"foo/", "bar", "foo/bar"},
		{"foo", "/bar", "foo/bar"},
		{"foo/", "/bar", "foo/bar"},
		{"foo//", "//bar", "foo/bar"},
		{"foo", "bar/", "foo/bar/"},
		{"foo/", "bar/", "foo/bar/"},
		{"foo", "/bar/", "foo/bar/"},
		{"foo/", "/bar/", "foo/bar/"},
		{"foo//", "//bar/", "foo/bar/"},
	}
	for i, tc := range testsFalse {
		t.Run(fmt.Sprintf("enabled StripSlashes #%d", i), func(t *testing.T) {
			got := singleJoiningSlash(tc.a, tc.b, false)
			assert.Equal(t, tc.want, got)
		})
	}
	testsTrue := []struct {
		a, b, want string
	}{
		{"", "", ""},
		{"/", "", "/"},
		{"", "/", ""},
		{"/", "/", "/"},
		{"foo", "", "foo"},
		{"foo", "/", "foo"},
		{"foo/", "", "foo/"},
		{"foo/", "/", "foo/"},
		{"foo/", "/name", "foo/name"},
		{"foo/", "/name/", "foo/name/"},
		{"foo/", "//name", "foo/name"},
		{"foo/", "//name/", "foo/name/"},
	}
	for i, tc := range testsTrue {
		t.Run(fmt.Sprintf("disabled StripSlashes #%d", i), func(t *testing.T) {
			got := singleJoiningSlash(tc.a, tc.b, true)
			assert.Equal(t, tc.want, got, fmt.Sprintf("a: %s, b: %s, out: %s, expected %s", tc.a, tc.b, got, tc.want))
		})
	}
}

func TestRequestIP(t *testing.T) {
	tests := []struct {
		remote, real, forwarded, want string
	}{
		// missing ip or port
		{want: ""},
		{remote: ":80", want: ""},
		{remote: "1.2.3.4", want: ""},
		{remote: "[::1]", want: ""},
		// no headers
		{remote: "1.2.3.4:80", want: "1.2.3.4"},
		{remote: "[::1]:80", want: "::1"},
		// real-ip
		{
			remote: "1.2.3.4:80",
			real:   "5.6.7.8",
			want:   "5.6.7.8",
		},
		{
			remote: "[::1]:80",
			real:   "::2",
			want:   "::2",
		},
		// forwarded-for
		{
			remote:    "1.2.3.4:80",
			forwarded: "5.6.7.8, px1, px2",
			want:      "5.6.7.8",
		},
		{
			remote:    "[::1]:80",
			forwarded: "::2",
			want:      "::2",
		},
		// both real-ip and forwarded-for
		{
			remote:    "1.2.3.4:80",
			real:      "5.6.7.8",
			forwarded: "4.3.2.1, px1, px2",
			want:      "5.6.7.8",
		},
	}
	for _, tc := range tests {
		r := &http.Request{RemoteAddr: tc.remote, Header: http.Header{}}
		r.Header.Set("x-real-ip", tc.real)
		r.Header.Set("x-forwarded-for", tc.forwarded)
		got := request.RealIP(r)
		if got != tc.want {
			t.Errorf("requestIP({%q, %q, %q}) got %q, want %q",
				tc.remote, tc.real, tc.forwarded, got, tc.want)
		}
	}
}

func TestCheckHeaderInRemoveList(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	type testSpec struct {
		UseExtendedPaths      bool
		GlobalHeadersRemove   []string
		ExtendedDeleteHeaders []string
	}
	tpl, err := texttemplate.New("test_tpl").Parse(`{
		"api_id": "1",
		"version_data": {
			"not_versioned": true,
			"versions": {
				"Default": {
					"name": "Default",
					"use_extended_paths": {{ .UseExtendedPaths }},
					"global_headers_remove": [{{ range $index, $hdr := .GlobalHeadersRemove }}{{if $index}}, {{end}}{{print "\"" . "\"" }}{{end}}],
					"extended_paths": {
						"transform_headers": [{
							"delete_headers": [{{range $index, $hdr := .ExtendedDeleteHeaders}}{{if $index}}, {{end}}{{print "\"" . "\""}}{{end}}],
							"path": "test",
							"method": "GET"
						}]
					}
				}
			}
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		header   string
		spec     testSpec
		expected bool
	}{
		{
			header: "X-Forwarded-For",
		},
		{
			header: "X-Forwarded-For",
			spec:   testSpec{GlobalHeadersRemove: []string{"X-Random-Header"}},
		},
		{
			header: "X-Forwarded-For",
			spec: testSpec{
				UseExtendedPaths:      true,
				ExtendedDeleteHeaders: []string{"X-Random-Header"},
			},
		},
		{
			header:   "X-Forwarded-For",
			spec:     testSpec{GlobalHeadersRemove: []string{"X-Forwarded-For"}},
			expected: true,
		},
		{
			header: "X-Forwarded-For",
			spec: testSpec{
				UseExtendedPaths:      true,
				GlobalHeadersRemove:   []string{"X-Random-Header"},
				ExtendedDeleteHeaders: []string{"X-Forwarded-For"},
			},
			expected: true,
		},
		{
			header: "X-Forwarded-For",
			spec: testSpec{
				UseExtendedPaths:      true,
				GlobalHeadersRemove:   []string{"X-Forwarded-For"},
				ExtendedDeleteHeaders: []string{"X-Forwarded-For"},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s:%t", tc.header, tc.expected), func(t *testing.T) {
			rp := &ReverseProxy{}
			r, err := http.NewRequest(http.MethodGet, "http://test/test", nil)
			if err != nil {
				t.Fatal(err)
			}

			var specOutput bytes.Buffer
			if err := tpl.Execute(&specOutput, tc.spec); err != nil {
				t.Fatal(err)
			}

			spec := ts.Gw.LoadSampleAPI(specOutput.String())
			actual := rp.CheckHeaderInRemoveList(tc.header, spec, r)
			if actual != tc.expected {
				t.Fatalf("want %t, got %t", tc.expected, actual)
			}
		})
	}
}

func testRequestIPHops(t testing.TB) {
	t.Helper()
	req := &http.Request{
		Header:     http.Header{},
		RemoteAddr: "test.com:80",
	}
	req.Header.Set("X-Forwarded-For", "abc")
	match := "abc, test.com"
	clientIP := requestIPHops(req)
	if clientIP != match {
		t.Fatalf("Got %s, expected %s", clientIP, match)
	}
}

func TestRequestIPHops(t *testing.T) {
	testRequestIPHops(t)
}

func TestNopCloseRequestBody(t *testing.T) {
	// try to pass nil request
	var req *http.Request
	nopCloseRequestBody(req)
	assert.Nil(t, req, "nil Request should remain nil")

	// try to pass nil body
	req = &http.Request{}
	nopCloseRequestBody(req)
	if req.Body != nil {
		t.Error("Request nil body should remain nil")
	}

	// try to pass not nil body and check that it was replaced with nopCloser
	req = httptest.NewRequest(http.MethodGet, "/test", strings.NewReader("abcxyz"))
	nopCloseRequestBody(req)
	if body, ok := req.Body.(*nopCloserBuffer); !ok {
		t.Error("Request's body was not replaced with nopCloser")
	} else {
		// try to read body 1st time
		if data, err := ioutil.ReadAll(body); err != nil {
			t.Error("1st read, error while reading body:", err)
		} else if !bytes.Equal(data, []byte("abcxyz")) { // compare with expected data
			t.Error("1st read, body's data is not as expectd")
		}

		// try to read body again without closing
		if data, err := ioutil.ReadAll(body); err != nil {
			t.Error("2nd read, error while reading body:", err)
		} else if !bytes.Equal(data, []byte("abcxyz")) { // compare with expected data
			t.Error("2nd read, body's data is not as expectd")
		}

		// close body and try to read "closed" one
		body.Close()
		if data, err := ioutil.ReadAll(body); err != nil {
			t.Error("3rd read, error while reading body:", err)
		} else if !bytes.Equal(data, []byte("abcxyz")) { // compare with expected data
			t.Error("3rd read, body's data is not as expectd")
		}
	}
}

func TestNopCloseResponseBody(t *testing.T) {
	var resp *http.Response
	nopCloseResponseBody(resp)
	assert.Nil(t, resp, "nil Response should remain nil")

	// try to pass nil body
	resp = &http.Response{}
	nopCloseResponseBody(resp)
	if resp.Body != nil {
		t.Error("Response nil body should remain nil")
	}

	// try to pass not nil body and check that it was replaced with nopCloser
	resp = &http.Response{}
	resp.Body = ioutil.NopCloser(strings.NewReader("abcxyz"))
	nopCloseResponseBody(resp)
	if body, ok := resp.Body.(*nopCloserBuffer); !ok {
		t.Error("Response's body was not replaced with nopCloser")
	} else {
		// try to read body 1st time
		if data, err := ioutil.ReadAll(body); err != nil {
			t.Error("1st read, error while reading body:", err)
		} else if !bytes.Equal(data, []byte("abcxyz")) { // compare with expected data
			t.Error("1st read, body's data is not as expectd")
		}

		// try to read body again without closing
		if data, err := ioutil.ReadAll(body); err != nil {
			t.Error("2nd read, error while reading body:", err)
		} else if !bytes.Equal(data, []byte("abcxyz")) { // compare with expected data
			t.Error("2nd read, body's data is not as expectd")
		}

		// close body and try to read "closed" one
		body.Close()
		if data, err := ioutil.ReadAll(body); err != nil {
			t.Error("3rd read, error while reading body:", err)
		} else if !bytes.Equal(data, []byte("abcxyz")) { // compare with expected data
			t.Error("3rd read, body's data is not as expectd")
		}
	}
}

func TestDeepCopyBody(t *testing.T) {
	var src *http.Request
	var trg *http.Request
	assert.Nil(t, deepCopyBody(src, trg), "nil requests should remain nil without any error")

	src = &http.Request{}
	trg = &http.Request{}
	assert.Nil(t, deepCopyBody(src, trg), "nil source request body should return without any error")

	testData := []byte("testDeepCopy")
	src = httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(testData))
	src.ContentLength = -1
	src.Header.Set("Content-Type", "application/grpc")
	assert.Nil(t, deepCopyBody(src, trg),
		"grpc request should return without any error")
	assert.Nil(t, trg.Body, "target request body should not be updated when it is grpc request")

	src.Header.Set("Connection", "Upgrade")
	assert.Nil(t, deepCopyBody(src, trg),
		"upgraded request should return without any error")
	assert.Nil(t, trg.Body, "target request body should not be updated when it is upgrade request")

	src = httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(testData))
	assert.Nil(t, deepCopyBody(src, trg), "request with body should return without any error")
	assert.NotNil(t, trg.Body, "target request body should be updated")
	assert.True(t, src.Body != trg.Body, "target request should have different body than source request")

	trgData, err := io.ReadAll(trg.Body)
	assert.Nil(t, err, "target request body should be readable")
	assert.Equal(t, testData, trgData, "target request body should contain the same data")

	mockReadCloser := createMockReadCloserWithError(errors.New("test error"))
	src = httptest.NewRequest(http.MethodPost, "/test", mockReadCloser)
	// Set any ContentLength - httptest.NewRequest sets it only for bytes.Buffer, bytes.Reader and strings.Reader
	src.ContentLength = int64(len(testData))
	trg = &http.Request{}
	err = deepCopyBody(src, trg)
	assert.NotNil(t, err, "function should return an error when ReadAll fails")
	assert.Nil(t, trg.Body, "target request body should not be updated when ReadAll fails")
	assert.True(t, mockReadCloser.CloseCalled, "close function should have been called")
	_, ok := src.Body.(*nopCloserBuffer)
	assert.True(t, ok, "target request body should have been of type nopCloserBuffer")
}

func BenchmarkGraphqlUDG(b *testing.B) {
	g := StartTest(func(globalConf *config.Config) {
		globalConf.OpenTelemetry.Enabled = true
	})
	b.Cleanup(g.Close)

	composedAPI := BuildAPI(func(spec *APISpec) {
		spec.Proxy.ListenPath = "/"
		spec.EnableContextVars = true
		spec.GraphQL.Enabled = true
		spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeExecutionEngine
		spec.GraphQL.Version = apidef.GraphQLConfigVersion2

		spec.GraphQL.Engine.DataSources = []apidef.GraphQLEngineDataSource{
			generateRESTDataSourceV2(func(ds *apidef.GraphQLEngineDataSource, restConfig *apidef.GraphQLEngineDataSourceConfigREST) {
				require.NoError(b, json.Unmarshal([]byte(testRESTHeadersDataSourceConfigurationV2), ds))
				require.NoError(b, json.Unmarshal(ds.Config, restConfig))
			}),
		}

		spec.GraphQL.TypeFieldConfigurations = nil
	})[0]

	g.Gw.LoadAPI(composedAPI)

	headers := graphql.Request{
		Query: "query Query { headers { name value } }",
	}

	for i := 0; i < b.N; i++ {
		_, _ = g.Run(b, []test.TestCase{
			{
				Data: headers,
				Code: http.StatusOK,
			},
		}...)
	}
}

func TestGraphQL_UDGHeaders(t *testing.T) {
	g := StartTest(nil)
	t.Cleanup(g.Close)

	composedAPI := BuildAPI(func(spec *APISpec) {
		spec.Proxy.ListenPath = "/"
		spec.EnableContextVars = true
		spec.GraphQL.Enabled = true
		spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeExecutionEngine
		spec.GraphQL.Version = apidef.GraphQLConfigVersion2
		spec.GraphQL.Engine.GlobalHeaders = []apidef.UDGGlobalHeader{
			{
				Key:   "Global-Static",
				Value: "foobar",
			},
			{
				Key:   "Global-Context",
				Value: "$tyk_context.headers_Global_From_Request",
			},
			{
				Key:   "Does-Exist-Already",
				Value: "global-does-exist-already",
			},
		}

		spec.GraphQL.Engine.DataSources = []apidef.GraphQLEngineDataSource{
			generateRESTDataSourceV2(func(ds *apidef.GraphQLEngineDataSource, restConfig *apidef.GraphQLEngineDataSourceConfigREST) {
				require.NoError(t, json.Unmarshal([]byte(testRESTHeadersDataSourceConfigurationV2), ds))
				require.NoError(t, json.Unmarshal(ds.Config, restConfig))
			}),
		}

		spec.GraphQL.TypeFieldConfigurations = nil
	})[0]

	g.Gw.LoadAPI(composedAPI)

	headers := graphql.Request{
		Query: "query Query { headers { name value } }",
	}

	// Test headers are gotten and updated for subsequent requests
	_, _ = g.Run(t, []test.TestCase{
		{
			Data: headers,
			Headers: map[string]string{
				"injected":            "FOO",
				"From-Request":        "request-context",
				"Global-From-Request": "request-global-context",
			},
			Code: http.StatusOK,

			BodyMatchFunc: func(b []byte) bool {
				return strings.Contains(string(b), `"headers":`) &&
					strings.Contains(string(b), `{"name":"Injected","value":"FOO"}`) &&
					strings.Contains(string(b), `{"name":"Static","value":"barbaz"}`) &&
					strings.Contains(string(b), `{"name":"Context","value":"request-context"}`) &&
					strings.Contains(string(b), `{"name":"Global-Static","value":"foobar"}`) &&
					strings.Contains(string(b), `{"name":"Global-Context","value":"request-global-context"}`) &&
					strings.Contains(string(b), `{"name":"Does-Exist-Already","value":"ds-does-exist-already"}`)
			},
		},
		{
			Data: headers,
			Headers: map[string]string{
				"injected":            "FOO",
				"From-Request":        "request-context",
				"Global-From-Request": "follow-up-request-global-context",
			},
			Code: http.StatusOK,
			BodyMatchFunc: func(b []byte) bool {
				return strings.Contains(string(b), `"headers":`) &&
					strings.Contains(string(b), `{"name":"Injected","value":"FOO"}`) &&
					strings.Contains(string(b), `{"name":"Static","value":"barbaz"}`) &&
					strings.Contains(string(b), `{"name":"Context","value":"request-context"}`) &&
					strings.Contains(string(b), `{"name":"Global-Static","value":"foobar"}`) &&
					strings.Contains(string(b), `{"name":"Global-Context","value":"follow-up-request-global-context"}`) &&
					strings.Contains(string(b), `{"name":"Does-Exist-Already","value":"ds-does-exist-already"}`)
			},
		},
	}...)
}

func TestGraphQL_ProxyOnlyHeaders(t *testing.T) {
	g := StartTest(nil)
	defer g.Close()

	defaultSpec := BuildAPI(func(spec *APISpec) {
		spec.Name = "tyk-api"
		spec.APIID = "tyk-api"
		spec.GraphQL.Enabled = true
		spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeProxyOnly
		spec.GraphQL.Schema = gqlCountriesSchema
		spec.GraphQL.Version = apidef.GraphQLConfigVersion2
		spec.Proxy.TargetURL = TestHttpAny + "/dynamic"
		spec.Proxy.ListenPath = "/"
	})[0]

	headerCheck := func(key, value string, headers map[string][]string) bool {
		val, ok := headers[key]
		return ok && len(val) > 0 && val[0] == value
	}

	t.Run("test introspection header", func(t *testing.T) {
		spec := defaultSpec
		spec.GraphQL.Proxy.AuthHeaders = map[string]string{
			"Test-Header": "test-value",
		}
		spec.GraphQL.Proxy.RequestHeaders = map[string]string{
			"Test-Request": "test-value",
		}
		g.Gw.LoadAPI(spec)
		g.AddDynamicHandler("/dynamic", func(writer http.ResponseWriter, r *http.Request) {
			if !headerCheck("Test-Request", "test-value", r.Header) {
				t.Error("request header missing")
			}
			if headerCheck("Test-Header", "test-value", r.Header) {
				t.Error("auth header missing")
			}
		})
		_, err := g.Run(t,
			test.TestCase{
				Path:   "/",
				Method: http.MethodPost,
				Data: graphql.Request{
					Query: gqlContinentQuery,
				},
			},
		)
		assert.NoError(t, err)
	})

	t.Run("test context variable request headers", func(t *testing.T) {
		spec := defaultSpec
		spec.GraphQL.Proxy.RequestHeaders = map[string]string{
			"Test-Request-Header": "$tyk_context.headers_Test_Header",
		}
		spec.EnableContextVars = true
		g.Gw.LoadAPI(spec)
		g.AddDynamicHandler("/dynamic", func(writer http.ResponseWriter, r *http.Request) {
			if !headerCheck("Test-Request-Header", "test-value", r.Header) {
				t.Error("context variable header missing/incorrect")
			}
		})
		_, err := g.Run(t, test.TestCase{
			Path: "/",
			Headers: map[string]string{
				"Test-Header": "test-value",
			},
			Method: http.MethodPost,
			Data: graphql.Request{
				Query: gqlContinentQuery,
			},
		})
		assert.NoError(t, err)
	})
}

func TestGraphQL_ProxyOnlyPassHeadersWithOTel(t *testing.T) {
	g := StartTest(func(globalConf *config.Config) {
		globalConf.OpenTelemetry.Enabled = true
	})
	defer g.Close()

	spec := BuildAPI(func(spec *APISpec) {
		spec.Name = "tyk-api"
		spec.APIID = "tyk-api"
		spec.GraphQL.Enabled = true
		spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeProxyOnly
		spec.GraphQL.Schema = gqlCountriesSchema
		spec.GraphQL.Version = apidef.GraphQLConfigVersion2
		spec.Proxy.TargetURL = TestHttpAny + "/dynamic"
		spec.Proxy.ListenPath = "/"
	})[0]

	g.Gw.LoadAPI(spec)
	g.AddDynamicHandler("/dynamic", func(writer http.ResponseWriter, r *http.Request) {
		if gotten := r.Header.Get("custom-client-header"); gotten != "custom-value" {
			t.Errorf("expected upstream to recieve header `custom-client-header` with value of `custom-value`, instead got %s", gotten)
		}
	})

	_, err := g.Run(t, test.TestCase{
		Path: "/",
		Headers: map[string]string{
			"custom-client-header": "custom-value",
		},
		Method: http.MethodPost,
		Data: graphql.Request{
			Query: gqlContinentQuery,
		},
	})

	assert.NoError(t, err)
}

func TestGraphQL_InternalDataSource(t *testing.T) {
	g := StartTest(nil)
	defer g.Close()

	tykGraphQL := BuildAPI(func(spec *APISpec) {
		spec.Name = "tyk-graphql"
		spec.APIID = "test1"
		spec.Proxy.TargetURL = testGraphQLDataSource
		spec.Proxy.ListenPath = "/tyk-graphql"
	})[0]

	tykREST := BuildAPI(func(spec *APISpec) {
		spec.Name = "tyk-rest"
		spec.APIID = "test2"
		spec.Proxy.TargetURL = testRESTDataSource
		spec.Proxy.ListenPath = "/tyk-rest"
	})[0]

	tykSubgraphAccounts := BuildAPI(func(spec *APISpec) {
		spec.Name = "subgraph-accounts"
		spec.APIID = "subgraph1"
		spec.Proxy.TargetURL = testSubgraphAccounts
		spec.Proxy.ListenPath = "/tyk-subgraph-accounts"
		spec.GraphQL = apidef.GraphQLConfig{
			Enabled:       true,
			ExecutionMode: apidef.GraphQLExecutionModeSubgraph,
			Version:       apidef.GraphQLConfigVersion2,
			Schema:        gqlSubgraphSchemaAccounts,
			Subgraph: apidef.GraphQLSubgraphConfig{
				SDL: gqlSubgraphSDLAccounts,
			},
		}
	})[0]

	tykSubgraphReviews := BuildAPI(func(spec *APISpec) {
		spec.Name = "subgraph-reviews"
		spec.APIID = "subgraph2"
		spec.Proxy.TargetURL = testSubgraphReviews
		spec.Proxy.ListenPath = "/tyk-subgraph-reviews"
		spec.GraphQL = apidef.GraphQLConfig{
			Enabled:       true,
			ExecutionMode: apidef.GraphQLExecutionModeSubgraph,
			Version:       apidef.GraphQLConfigVersion2,
			Schema:        gqlSubgraphSchemaReviews,
			Subgraph: apidef.GraphQLSubgraphConfig{
				SDL: gqlSubgraphSDLReviews,
			},
		}
	})[0]

	t.Run("supergraph (engine v2)", func(t *testing.T) {
		supergraph := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.APIID = "supergraph"
			spec.GraphQL = apidef.GraphQLConfig{
				Enabled:       true,
				Version:       apidef.GraphQLConfigVersion2,
				ExecutionMode: apidef.GraphQLExecutionModeSupergraph,
				Supergraph: apidef.GraphQLSupergraphConfig{
					Subgraphs: []apidef.GraphQLSubgraphEntity{
						{
							APIID: "subgraph1",
							URL:   "tyk://" + tykSubgraphAccounts.Name,
							SDL:   gqlSubgraphSDLAccounts,
						},
						{
							APIID: "subgraph2",
							URL:   "tyk://" + tykSubgraphReviews.Name,
							SDL:   gqlSubgraphSDLReviews,
						},
					},
					MergedSDL: gqlMergedSupergraphSDL,
				},
				Schema: gqlMergedSupergraphSDL,
			}
		})[0]

		g.Gw.LoadAPI(tykSubgraphAccounts, tykSubgraphReviews, supergraph)

		reviews := graphql.Request{
			Query: `query Query { me { id username reviews { body } } }`,
		}

		_, _ = g.Run(t, []test.TestCase{
			{Data: reviews, BodyMatch: `{"data":{"me":{"id":"1","username":"tyk","reviews":\[{"body":"A highly effective form of birth control."},{"body":"Fedoras are one of the most fashionable hats around and can look great with a variety of outfits."}\]}}}`, Code: http.StatusOK},
		}...)
	})

	t.Run("graphql engine v2", func(t *testing.T) {
		composedAPI := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.APIID = "test3"
			spec.GraphQL.Enabled = true
			spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeExecutionEngine
			spec.GraphQL.Version = apidef.GraphQLConfigVersion2
			spec.GraphQL.Engine.DataSources[0] = generateRESTDataSourceV2(func(_ *apidef.GraphQLEngineDataSource, restConfig *apidef.GraphQLEngineDataSourceConfigREST) {
				restConfig.URL = fmt.Sprintf("tyk://%s", tykREST.Name)
			})
			spec.GraphQL.Engine.DataSources[1] = generateGraphQLDataSourceV2(func(_ *apidef.GraphQLEngineDataSource, graphqlConf *apidef.GraphQLEngineDataSourceConfigGraphQL) {
				graphqlConf.URL = fmt.Sprintf("tyk://%s", tykGraphQL.Name)
			})
			spec.GraphQL.TypeFieldConfigurations = nil
		})[0]

		g.Gw.LoadAPI(tykGraphQL, tykREST, composedAPI)

		countries := graphql.Request{
			Query: "query Query { countries { name } }",
		}

		people := graphql.Request{
			Query: "query Query { people { name } }",
		}

		_, _ = g.Run(t, []test.TestCase{
			// GraphQL Data Source
			{Data: countries, BodyMatch: `"countries":.*{"name":"Turkey"},{"name":"Russia"}.*`, Code: http.StatusOK},

			// REST Data Source
			{Data: people, BodyMatch: `"people":.*{"name":"Furkan"},{"name":"Leo"}.*`, Code: http.StatusOK},
		}...)
	})

	t.Run("graphql engine v1", func(t *testing.T) {
		composedAPI := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.APIID = "test4"
			spec.GraphQL.Enabled = true
			spec.GraphQL.TypeFieldConfigurations[0].DataSource.Config = generateGraphQLDataSource(func(graphQLDataSource *datasource.GraphQLDataSourceConfig) {
				graphQLDataSource.URL = fmt.Sprintf("tyk://%s", tykGraphQL.Name)
			})
			spec.GraphQL.TypeFieldConfigurations[1].DataSource.Config = generateRESTDataSource(func(restDataSource *datasource.HttpJsonDataSourceConfig) {
				restDataSource.URL = fmt.Sprintf("tyk://%s", tykREST.Name)
			})
		})[0]

		g.Gw.LoadAPI(tykGraphQL, tykREST, composedAPI)

		countries := graphql.Request{
			Query: "query Query { countries { name } }",
		}

		people := graphql.Request{
			Query: "query Query { people { name } }",
		}

		_, _ = g.Run(t, []test.TestCase{
			// GraphQL Data Source
			{Data: countries, BodyMatch: `"countries":.*{"name":"Turkey"},{"name":"Russia"}.*`, Code: http.StatusOK},

			// REST Data Source
			{Data: people, BodyMatch: `"people":.*{"name":"Furkan"},{"name":"Leo"}.*`, Code: http.StatusOK},
		}...)
	})
}

func TestGraphQL_SubgraphBatchRequest(t *testing.T) {
	g := StartTest(nil)
	t.Cleanup(func() {
		g.Close()
	})

	bankAccountSubgraphPath := "/subgraph-bank-accounts"
	tykSubgraphAccounts := BuildAPI(func(spec *APISpec) {
		spec.Name = "subgraph-accounts-modified"
		spec.APIID = "subgraph-accounts-modified"
		spec.Proxy.TargetURL = testSubgraphAccountsModified
		spec.Proxy.ListenPath = "/tyk-subgraph-accounts-modified"
		spec.GraphQL = apidef.GraphQLConfig{
			Enabled:       true,
			ExecutionMode: apidef.GraphQLExecutionModeSubgraph,
			Version:       apidef.GraphQLConfigVersion2,
			Schema:        gqlSubgraphSchemaAccounts,
			Subgraph: apidef.GraphQLSubgraphConfig{
				SDL: gqlSubgraphSDLAccounts,
			},
		}
	})[0]
	tykSubgraphBankAccounts := BuildAPI(func(spec *APISpec) {
		spec.Name = "subgraph-bank-accounts"
		spec.APIID = "subgraph-bank-accounts"
		spec.Proxy.TargetURL = TestHttpAny + bankAccountSubgraphPath
		spec.Proxy.ListenPath = "/subgraph-bank-accounts"
		spec.GraphQL = apidef.GraphQLConfig{
			Enabled:       true,
			ExecutionMode: apidef.GraphQLExecutionModeSubgraph,
			Version:       apidef.GraphQLConfigVersion2,
			Schema:        gqlSubgraphSchemaBankAccounts,
			Subgraph: apidef.GraphQLSubgraphConfig{
				SDL: gqlSubgraphSDLBankAccounts,
			},
		}
	})[0]

	t.Run("should batch requests", func(t *testing.T) {
		supergraph := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/batched-supergraph"
			spec.APIID = "batched-supergraph"
			spec.GraphQL = apidef.GraphQLConfig{
				Enabled:       true,
				Version:       apidef.GraphQLConfigVersion2,
				ExecutionMode: apidef.GraphQLExecutionModeSupergraph,
				Supergraph: apidef.GraphQLSupergraphConfig{
					Subgraphs: []apidef.GraphQLSubgraphEntity{
						{
							APIID: "subgraph-accounts-modified",
							URL:   "tyk://" + tykSubgraphAccounts.Name,
							SDL:   gqlSubgraphSDLAccounts,
						},
						{
							APIID: "subgraph-bank-accounts",
							URL:   "tyk://" + tykSubgraphBankAccounts.Name,
							SDL:   gqlSubgraphSDLBankAccounts,
						},
					},
					MergedSDL: gqlMergedSupergraphSDL,
				},
				Schema: gqlMergedSupergraphSDL,
			}
		})[0]
		g.Gw.LoadAPI(tykSubgraphAccounts, tykSubgraphBankAccounts, supergraph)
		handlerCtx, cancel := context.WithCancel(context.Background())
		g.AddDynamicHandler(bankAccountSubgraphPath, func(writer http.ResponseWriter, r *http.Request) {
			select {
			case <-handlerCtx.Done():
				assert.Fail(t, "Called twice time")
			default:
			}
			cancel()
		})

		q := graphql.Request{
			Query: `query Query { allUsers { id username account { number } } }`,
		}

		_, _ = g.Run(t, []test.TestCase{
			{
				Data: q, Path: "/batched-supergraph",
			},
		}...)
	})

	t.Run("shouldn't batch requests", func(t *testing.T) {
		supergraph := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/unbatched-supergraph"
			spec.APIID = "unbatched-supergraph"
			spec.GraphQL = apidef.GraphQLConfig{
				Enabled:       true,
				Version:       apidef.GraphQLConfigVersion2,
				ExecutionMode: apidef.GraphQLExecutionModeSupergraph,
				Supergraph: apidef.GraphQLSupergraphConfig{
					DisableQueryBatching: true,
					Subgraphs: []apidef.GraphQLSubgraphEntity{
						{
							APIID: "subgraph-accounts-modified",
							URL:   "tyk://" + tykSubgraphAccounts.Name,
							SDL:   gqlSubgraphSDLAccounts,
						},
						{
							APIID: "subgraph-bank-accounts",
							URL:   "tyk://" + tykSubgraphBankAccounts.Name,
							SDL:   gqlSubgraphSDLBankAccounts,
						},
					},
					MergedSDL: gqlMergedSupergraphSDL,
				},
				Schema: gqlMergedSupergraphSDL,
			}
		})[0]
		g.Gw.LoadAPI(tykSubgraphAccounts, tykSubgraphBankAccounts, supergraph)
		timesHit := 0
		lock := sync.Mutex{}
		g.AddDynamicHandler(bankAccountSubgraphPath, func(writer http.ResponseWriter, r *http.Request) {
			lock.Lock()
			timesHit++
			lock.Unlock()
		})

		q := graphql.Request{
			Query: `query Query { allUsers { id username account { number } } }`,
		}
		// run this in a goroutine to prevent blocking, we don't actually need the test to match body or response
		go func() {
			_, _ = g.Run(t, []test.TestCase{
				{
					Data: q, Path: "/unbatched-supergraph",
				},
			}...)
		}()

		assert.Eventually(t, func() bool {
			lock.Lock()
			defer lock.Unlock()
			return timesHit == 2
		}, time.Second*5, time.Millisecond*100)
	})
}

func TestGraphQL_InternalDataSource_memConnProviders(t *testing.T) {
	g := StartTest(nil)
	defer g.Close()

	// tests run in parallel and memConnProviders is a global struct.
	// For consistency, we use unique names for the subgraphs.
	tykSubgraphAccounts := BuildAPI(func(spec *APISpec) {
		spec.Name = fmt.Sprintf("subgraph-accounts-%d", mathrand.Intn(1000))
		spec.APIID = "subgraph1"
		spec.Proxy.TargetURL = testSubgraphAccounts
		spec.Proxy.ListenPath = "/tyk-subgraph-accounts"
		spec.GraphQL = apidef.GraphQLConfig{
			Enabled:       true,
			ExecutionMode: apidef.GraphQLExecutionModeSubgraph,
			Version:       apidef.GraphQLConfigVersion2,
			Schema:        gqlSubgraphSchemaAccounts,
			Subgraph: apidef.GraphQLSubgraphConfig{
				SDL: gqlSubgraphSDLAccounts,
			},
		}
	})[0]

	tykSubgraphReviews := BuildAPI(func(spec *APISpec) {
		spec.Name = fmt.Sprintf("subgraph-reviews-%d", mathrand.Intn(1000))
		spec.APIID = "subgraph2"
		spec.Proxy.TargetURL = testSubgraphReviews
		spec.Proxy.ListenPath = "/tyk-subgraph-reviews"
		spec.GraphQL = apidef.GraphQLConfig{
			Enabled:       true,
			ExecutionMode: apidef.GraphQLExecutionModeSubgraph,
			Version:       apidef.GraphQLConfigVersion2,
			Schema:        gqlSubgraphSchemaReviews,
			Subgraph: apidef.GraphQLSubgraphConfig{
				SDL: gqlSubgraphSDLReviews,
			},
		}
	})[0]

	supergraph := BuildAPI(func(spec *APISpec) {
		spec.Proxy.ListenPath = "/"
		spec.APIID = "supergraph"
		spec.GraphQL = apidef.GraphQLConfig{
			Enabled:       true,
			Version:       apidef.GraphQLConfigVersion2,
			ExecutionMode: apidef.GraphQLExecutionModeSupergraph,
			Supergraph: apidef.GraphQLSupergraphConfig{
				Subgraphs: []apidef.GraphQLSubgraphEntity{
					{
						APIID: "subgraph1",
						URL:   "tyk://" + tykSubgraphAccounts.Name,
						SDL:   gqlSubgraphSDLAccounts,
					},
					{
						APIID: "subgraph2",
						URL:   "tyk://" + tykSubgraphReviews.Name,
						SDL:   gqlSubgraphSDLReviews,
					},
				},
				MergedSDL: gqlMergedSupergraphSDL,
			},
			Schema: gqlMergedSupergraphSDL,
		}
	})[0]

	g.Gw.LoadAPI(tykSubgraphAccounts, tykSubgraphReviews, supergraph)

	reviews := graphql.Request{
		Query: `query Query { me { id username reviews { body } } }`,
	}

	_, _ = g.Run(t, []test.TestCase{
		{Data: reviews, BodyMatch: `{"data":{"me":{"id":"1","username":"tyk","reviews":\[{"body":"A highly effective form of birth control."},{"body":"Fedoras are one of the most fashionable hats around and can look great with a variety of outfits."}\]}}}`, Code: http.StatusOK},
	}...)

	memConnProviders.mtx.Lock()
	require.Contains(t, memConnProviders.m, tykSubgraphAccounts.Name)
	require.Contains(t, memConnProviders.m, tykSubgraphReviews.Name)
	memConnProviders.mtx.Unlock()

	// Remove memconn.Provider structs from the cache, if they are idle for a while.
	cleanIdleMemConnProvidersEagerly(time.Now().Add(2 * time.Minute))

	memConnProviders.mtx.Lock()
	require.NotContains(t, memConnProviders.m, tykSubgraphAccounts.Name)
	require.NotContains(t, memConnProviders.m, tykSubgraphReviews.Name)
	memConnProviders.mtx.Unlock()
}

func TestGraphQL_ProxyIntrospectionInterrupt(t *testing.T) {
	g := StartTest(nil)
	defer g.Close()

	g.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.GraphQL.Enabled = true
		spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeProxyOnly
		spec.GraphQL.Schema = "schema { query: query_root } type query_root { hello: String }"
		spec.Proxy.ListenPath = "/"
	})

	t.Run("introspection request should be interrupted", func(t *testing.T) {
		namedIntrospection := graphql.Request{
			OperationName: "IntrospectionQuery",
			Query:         gqlIntrospectionQuery,
		}

		silentIntrospection := graphql.Request{
			OperationName: "",
			Query:         strings.Replace(gqlIntrospectionQuery, "query IntrospectionQuery ", "", 1),
		}

		_, _ = g.Run(t, []test.TestCase{
			{Data: namedIntrospection, BodyMatch: `"name":"query_root"`, Code: http.StatusOK},
			{Data: silentIntrospection, BodyMatch: `"name":"query_root"`, Code: http.StatusOK},
		}...)
	})

	t.Run("normal requests should be proxied", func(t *testing.T) {
		validRequest := graphql.Request{
			Query: "query { hello }",
		}

		_, _ = g.Run(t, []test.TestCase{
			{Data: validRequest, BodyMatch: `"Headers":{"Accept-Encoding"`, Code: http.StatusOK},
		}...)
	})
}

func TestGraphQL_OptionsPassThrough(t *testing.T) {
	g := StartTest(nil)
	defer g.Close()

	var headers = map[string]string{
		"Host":                           g.URL,
		"Connection":                     "keep-alive",
		"Accept":                         "*/*",
		"Access-Control-Request-Method":  http.MethodPost,
		"Access-Control-Request-Headers": "content-type",
		"Origin":                         "http://192.168.1.123:3000",
		"User-Agent":                     "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.114 Safari/537.36",
		"Sec-Fetch-Mode":                 "cors",
		"Referer":                        "http://192.168.1.123:3000/",
		"Accept-Encoding":                "gzip, deflate",
		"Accept-Language":                "en-US,en;q=0.9",
	}

	t.Run("ProxyOnly should pass through", func(t *testing.T) {
		g.Gw.BuildAndLoadAPI(func(spec *APISpec) {
			spec.GraphQL.Enabled = true
			spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeProxyOnly
			spec.GraphQL.Schema = "schema { query: query_root } type query_root { hello: String }"
			spec.Proxy.ListenPath = "/starwars"
			spec.CORS = apidef.CORSConfig{
				Enable:             true,
				OptionsPassthrough: true,
			}
		})
		_, _ = g.Run(t, test.TestCase{
			Method:  http.MethodOptions,
			Path:    "/starwars",
			Headers: headers,
			Code:    http.StatusOK,
		})
	})
	t.Run("UDG should not pass through", func(t *testing.T) {
		g.Gw.BuildAndLoadAPI(func(spec *APISpec) {
			spec.GraphQL.Enabled = true
			spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeExecutionEngine
			spec.GraphQL.Schema = "schema { query: query_root } type query_root { hello: String }"
			spec.Proxy.ListenPath = "/starwars-udg"
			spec.CORS = apidef.CORSConfig{
				Enable:             true,
				OptionsPassthrough: true,
			}
		})
		_, _ = g.Run(t, test.TestCase{
			Method:  http.MethodOptions,
			Path:    "/starwars-udg",
			Headers: headers,
			Code:    http.StatusInternalServerError,
		})
	})
	t.Run("Supergraph should not pass through", func(t *testing.T) {
		g.Gw.BuildAndLoadAPI(func(spec *APISpec) {
			spec.GraphQL.Enabled = true
			spec.GraphQL.ExecutionMode = apidef.GraphQLExecutionModeExecutionEngine
			spec.GraphQL.Schema = "schema { query: query_root } type query_root { hello: String }"
			spec.Proxy.ListenPath = "/starwars-supergraph"
			spec.CORS = apidef.CORSConfig{
				Enable:             true,
				OptionsPassthrough: true,
			}
		})
		_, _ = g.Run(t, test.TestCase{
			Method:  http.MethodOptions,
			Path:    "/starwars-supergraph",
			Headers: headers,
			Code:    http.StatusInternalServerError,
		})
	})
}

func BenchmarkRequestIPHops(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		testRequestIPHops(b)
	}
}

func BenchmarkWrappedServeHTTP(b *testing.B) {
	b.ReportAllocs()

	ts := StartTest(nil)
	defer ts.Close()

	proxy := ts.TestNewWrappedServeHTTP()
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	for i := 0; i < b.N; i++ {
		proxy.WrappedServeHTTP(recorder, req, false)
	}
}

func BenchmarkCopyRequestResponse(b *testing.B) {

	// stress test this with 20mb payload
	str := strings.Repeat("x!", 10000000)

	b.ReportAllocs()

	req := &http.Request{}
	res := &http.Response{}
	for i := 0; i < b.N; i++ {
		req.Body = ioutil.NopCloser(strings.NewReader(str))
		res.Body = ioutil.NopCloser(strings.NewReader(str))
		for j := 0; j < 10; j++ {
			req, _ = copyRequest(req)
			res, _ = copyResponse(res)
		}
	}
}

func TestEnsureTransport(t *testing.T) {
	cases := []struct {
		host, protocol, expect string
	}{
		// This section tests EnsureTransport if port + IP is supplied
		{"https://192.168.1.1:443 ", "https", "https://192.168.1.1:443"},
		{"192.168.1.1:443 ", "https", "https://192.168.1.1:443"},
		{"http://192.168.1.1:80 ", "https", "http://192.168.1.1:80"},
		{"192.168.1.1:2000 ", "tls", "tls://192.168.1.1:2000"},
		{"192.168.1.1:2000 ", "", "http://192.168.1.1:2000"},
		// This section tests EnsureTransport if port is supplied
		{"https://httpbin.org:443 ", "https", "https://httpbin.org:443"},
		{"httpbin.org:443 ", "https", "https://httpbin.org:443"},
		{"http://httpbin.org:80 ", "https", "http://httpbin.org:80"},
		{"httpbin.org:2000 ", "tls", "tls://httpbin.org:2000"},
		{"httpbin.org:2000 ", "", "http://httpbin.org:2000"},
		// This is the h2c proto to http conversion
		{"http://httpbin.org ", "h2c", "http://httpbin.org"},
		{"h2c://httpbin.org ", "h2c", "http://httpbin.org"},
		{"httpbin.org ", "h2c", "http://httpbin.org"},
		// This is the default parse section
		{"https://httpbin.org ", "https", "https://httpbin.org"},
		{"httpbin.org ", "https", "https://httpbin.org"},
		{"http://httpbin.org ", "https", "http://httpbin.org"},
		{"httpbin.org ", "tls", "tls://httpbin.org"},
		{"httpbin.org ", "", "http://httpbin.org"},
	}
	for i, v := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			g := EnsureTransport(v.host, v.protocol)

			assert.Equal(t, v.expect, g)

			_, err := url.Parse(g)
			assert.NoError(t, err)
		})
	}
}

func TestReverseProxyWebSocketCancelation(t *testing.T) {
	conf := func(globalConf *config.Config) {
		globalConf.HttpServerOptions.EnableWebSockets = true
	}
	ts := StartTest(conf)
	defer ts.Close()

	n := 5
	triggerCancelCh := make(chan bool, n)
	nthResponse := func(i int) string {
		return fmt.Sprintf("backend response #%d\n", i)
	}
	terminalMsg := "final message"

	cst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g, ws := upgradeType(r.Header), "websocket"; g != ws {
			t.Errorf("Unexpected upgrade type %q, want %q", g, ws)
			http.Error(w, "Unexpected request", 400)
			return
		}
		conn, bufrw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()

		upgradeMsg := "HTTP/1.1 101 Switching Protocols\r\nConnection: upgrade\r\nUpgrade: WebSocket\r\n\r\n"
		if _, err := io.WriteString(conn, upgradeMsg); err != nil {
			t.Error(err)
			return
		}
		if _, _, err := bufrw.ReadLine(); err != nil {
			t.Errorf("Failed to read line from client: %v", err)
			return
		}

		for i := 0; i < n; i++ {
			if _, err := bufrw.WriteString(nthResponse(i)); err != nil {
				select {
				case <-triggerCancelCh:
				default:
					t.Errorf("Writing response #%d failed: %v", i, err)
				}
				return
			}
			bufrw.Flush()
			time.Sleep(20 * time.Millisecond)
		}
		if _, err := bufrw.WriteString(terminalMsg); err != nil {
			select {
			case <-triggerCancelCh:
			default:
				t.Errorf("Failed to write terminal message: %v", err)
			}
		}
		bufrw.Flush()
	}))
	defer cst.Close()

	backendURL, _ := url.Parse(cst.URL)
	spec := &APISpec{APIDefinition: &apidef.APIDefinition{}}
	rproxy := ts.Gw.TykNewSingleHostReverseProxy(backendURL, spec, nil)

	handler := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("X-Header", "X-Value")
		ctx, cancel := context.WithCancel(req.Context())
		go func() {
			<-triggerCancelCh
			cancel()
		}()
		rproxy.ServeHTTP(rw, req.WithContext(ctx))
	})

	frontendProxy := httptest.NewServer(handler)
	defer frontendProxy.Close()

	req, _ := http.NewRequest("GET", frontendProxy.URL, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	res, err := frontendProxy.Client().Do(req)
	if err != nil {
		t.Fatalf("Dialing to frontend proxy: %v", err)
	}
	defer res.Body.Close()
	if g, w := res.StatusCode, 101; g != w {
		t.Fatalf("Switching protocols failed, got: %d, want: %d", g, w)
	}

	if g, w := res.Header.Get("X-Header"), "X-Value"; g != w {
		t.Errorf("X-Header mismatch\n\tgot:  %q\n\twant: %q", g, w)
	}

	if g, w := upgradeType(res.Header), "websocket"; g != w {
		t.Fatalf("Upgrade header mismatch\n\tgot:  %q\n\twant: %q", g, w)
	}

	rwc, ok := res.Body.(io.ReadWriteCloser)
	if !ok {
		t.Fatalf("Response body type mismatch, got %T, want io.ReadWriteCloser", res.Body)
	}

	if _, err := io.WriteString(rwc, "Hello\n"); err != nil {
		t.Fatalf("Failed to write first message: %v", err)
	}

	// Read loop.

	br := bufio.NewReader(rwc)
	for {
		line, err := br.ReadString('\n')
		switch {
		case line == terminalMsg: // this case before "err == io.EOF"
			t.Fatalf("The websocket request was not canceled, unfortunately!")

		case errors.Is(err, io.EOF):
			return

		case err != nil:
			t.Fatalf("Unexpected error: %v", err)

		case line == nthResponse(0): // We've gotten the first response back
			// Let's trigger a cancel.
			close(triggerCancelCh)
		}
	}
}

func TestSSE(t *testing.T) {
	sseServer := TestHelperSSEServer(t)
	conf := func(globalConf *config.Config) {
		globalConf.HttpServerOptions.EnableWebSockets = false
	}
	ts := StartTest(conf)
	defer ts.Close()

	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.Proxy.TargetURL = sseServer.URL
		spec.Proxy.ListenPath = "/"
	})

	t.Run("websockets disabled", func(t *testing.T) {
		assert.NoError(t, TestHelperSSEStreamClient(t, ts, false))
	})

	t.Run("websockets enabled", func(t *testing.T) {
		assert.NoError(t, TestHelperSSEStreamClient(t, ts, true))
	})

	t.Run("sse streaming with detailed recording enabled", func(t *testing.T) {
		sseServer := TestHelperSSEServer(t)
		ts := StartTest(func(c *config.Config) {
			c.AnalyticsConfig.EnableDetailedRecording = true
		})

		t.Cleanup(ts.Close)
		ts.Gw.Analytics.Flush()

		var activityCounter atomic.Int32

		ts.Gw.Analytics.mockEnabled = true
		ts.Gw.Analytics.mockRecordHit = func(record *analytics.AnalyticsRecord) {
			activityCounter.Add(1)
		}

		ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
			spec.Proxy.TargetURL = sseServer.URL
			spec.Proxy.ListenPath = "/"
			spec.EnableDetailedRecording = true
			spec.UseKeylessAccess = true
		})

		require.NoError(t, TestHelperSSEStreamClient(t, ts, false))
		assert.Equal(t, int32(1), activityCounter.Load())
	})
}

func TestSetCustomHeaderMultipleValues(t *testing.T) {
	tests := []struct {
		name            string
		headers         http.Header
		key             string
		values          []string
		ignoreCanonical bool
		want            http.Header
	}{
		{
			name:            "Add multiple values without canonical form",
			headers:         http.Header{},
			key:             "X-Test",
			values:          []string{"value1", "value2"},
			ignoreCanonical: true,
			want:            http.Header{"X-Test": {"value1", "value2"}},
		},
		{
			name:            "Add multiple values with canonical form",
			headers:         http.Header{},
			key:             "X-Test",
			values:          []string{"value1", "value2"},
			ignoreCanonical: false,
			want:            http.Header{"X-Test": {"value1", "value2"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setCustomHeaderMultipleValues(tt.headers, tt.key, tt.values, tt.ignoreCanonical)
			if !reflect.DeepEqual(tt.headers, tt.want) {
				t.Errorf("setCustomHeaderMultipleValues() got = %v, want %v", tt.headers, tt.want)
			}
		})
	}
}

func TestCreateMemConnProviderIfNeeded(t *testing.T) {
	t.Run("should propagate context", func(t *testing.T) {
		propagationContext := context.WithValue(context.Background(), "parentContextKey", "parentContextValue")
		propagationContextWithCancel, cancel := context.WithCancel(propagationContext)
		internalReq, err := http.NewRequest(http.MethodGet, "http://memoryhost/", nil)
		require.NoError(t, err)

		handler := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			assert.Equal(t, "parentContextValue", req.Context().Value("parentContextKey"))
			cancel()
		})

		err = createMemConnProviderIfNeeded(handler, internalReq.WithContext(propagationContextWithCancel))
		require.NoError(t, err)

		assert.Eventuallyf(t, func() bool {
			testReq, err := http.NewRequest(http.MethodGet, "http://memoryhost/", nil)
			require.NoError(t, err)
			_, err = memConnClient.Do(testReq)
			require.NoError(t, err)
			<-propagationContextWithCancel.Done()
			return true
		}, time.Second, time.Millisecond*25, "context was not canceled")
	})
}

func TestQuotaResponseHeaders(t *testing.T) {
	ts := StartTest(nil)
	defer ts.Close()

	spec := ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.Proxy.ListenPath = "/quota-headers-test"
		spec.UseKeylessAccess = false
	})[0]

	var (
		quotaMax, quotaRenewalRate int64 = 2, 3600
	)

	assertQuota := func(t *testing.T, ts *Test, key string) {
		t.Helper()

		authorization := map[string]string{
			"Authorization": key,
		}

		_, _ = ts.Run(t, []test.TestCase{
			{
				Headers: authorization,
				Path:    "/quota-headers-test/",
				Code:    http.StatusOK,
				HeadersMatch: map[string]string{
					header.XRateLimitLimit:     fmt.Sprintf("%d", quotaMax),
					header.XRateLimitRemaining: fmt.Sprintf("%d", quotaMax-1),
				},
			},
			{
				Headers: authorization,
				Path:    "/quota-headers-test/",
				Code:    http.StatusOK,
				HeadersMatch: map[string]string{
					header.XRateLimitLimit:     fmt.Sprintf("%d", quotaMax),
					header.XRateLimitRemaining: fmt.Sprintf("%d", quotaMax-2),
				},
			},
			{
				Headers: authorization,
				Path:    "/quota-headers-test/abc",
				Code:    http.StatusForbidden,
			},
		}...)
	}

	t.Run("key without policy", func(t *testing.T) {
		_, authKey := ts.CreateSession(func(s *user.SessionState) {
			s.AccessRights = map[string]user.AccessDefinition{
				spec.APIID: {
					APIName:  spec.Name,
					APIID:    spec.APIID,
					Versions: []string{"default"},
					Limit: user.APILimit{
						QuotaMax:         quotaMax,
						QuotaRenewalRate: quotaRenewalRate,
					},
					AllowanceScope: spec.APIID,
				},
			}
			s.OrgID = spec.OrgID
		})
		assertQuota(t, ts, authKey)
	})

	t.Run("key from policy with per api limits", func(t *testing.T) {
		polID := ts.CreatePolicy(func(p *user.Policy) {
			p.Name = "p1"
			p.KeyExpiresIn = 3600
			p.Partitions = user.PolicyPartitions{
				PerAPI: true,
			}
			p.OrgID = spec.OrgID
			p.AccessRights = map[string]user.AccessDefinition{
				spec.APIID: {
					APIName:  spec.Name,
					APIID:    spec.APIID,
					Versions: []string{"default"},
					Limit: user.APILimit{
						QuotaMax:         quotaMax,
						QuotaRenewalRate: quotaRenewalRate,
					},
					AllowanceScope: spec.APIID,
				},
			}
		})

		_, policyKey := ts.CreateSession(func(s *user.SessionState) {
			s.ApplyPolicies = []string{polID}
		})

		assertQuota(t, ts, policyKey)
	})

	t.Run("key from policy with global limits", func(t *testing.T) {
		polID := ts.CreatePolicy(func(p *user.Policy) {
			p.Name = "p1"
			p.KeyExpiresIn = 3600
			p.Partitions = user.PolicyPartitions{
				Quota:     true,
				RateLimit: true,
				Acl:       true,
			}
			p.OrgID = spec.OrgID
			p.QuotaMax = quotaMax
			p.QuotaRenewalRate = quotaRenewalRate
			p.AccessRights = map[string]user.AccessDefinition{
				spec.APIID: {
					APIName:        spec.Name,
					APIID:          spec.APIID,
					Versions:       []string{"default"},
					Limit:          user.APILimit{},
					AllowanceScope: spec.APIID,
				},
			}
		})

		_, policyKey := ts.CreateSession(func(s *user.SessionState) {
			s.ApplyPolicies = []string{polID}
		})

		assertQuota(t, ts, policyKey)
	})

}

func BenchmarkLargeResponsePayload(b *testing.B) {
	ts := StartTest(func(_ *config.Config) {})
	b.Cleanup(ts.Close)

	// Create a 500 MB payload of zeros
	payloadSize := 500 * 1024 * 1024 // 500 MB in bytes
	largePayload := bytes.Repeat([]byte("x"), payloadSize)

	largePayloadHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(payloadSize))
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(largePayload)
		assert.NoError(b, err)
	}

	// Create a test server with the largePayloadHandler
	testServer := httptest.NewServer(http.HandlerFunc(largePayloadHandler))
	b.Cleanup(testServer.Close)

	ts.Gw.BuildAndLoadAPI(func(spec *APISpec) {
		spec.UseKeylessAccess = true
		spec.Proxy.ListenPath = "/"
		spec.Proxy.TargetURL = testServer.URL
	})

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ts.Run(b, test.TestCase{
			Method: http.MethodGet,
			Path:   "/",
			Code:   http.StatusOK,
		})
	}
}

func TestTimeoutPrioritization(t *testing.T) {
	t.Parallel()

	ts := StartTest(func(c *config.Config) {
		c.ProxyDefaultTimeout = 2
	})
	defer ts.Close()

	t.Run("Basic Timeout Behavior - enforced timeout higher than default", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(1 * time.Second)
			w.Write([]byte("Success"))
		}))
		defer upstream.Close()

		api := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.Proxy.TargetURL = upstream.URL
			spec.UseKeylessAccess = true
			spec.EnforcedTimeoutEnabled = true
			UpdateAPIVersion(spec, "", func(version *apidef.VersionInfo) {
				version.UseExtendedPaths = true
				version.ExtendedPaths.HardTimeouts = []apidef.HardTimeoutMeta{
					{
						Disabled: false,
						Path:     "/test1",
						Method:   http.MethodGet,
						TimeOut:  4,
					},
				}
			})
		})[0]

		ts.Gw.LoadAPI(api)

		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/test1",
			Code:      http.StatusOK,
			BodyMatch: "Success",
		})
	})

	t.Run("Basic Timeout Behavior - enforced timeout lower than default", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(3 * time.Second)
			w.Write([]byte("Success"))
		}))
		defer upstream.Close()

		api := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.Proxy.TargetURL = upstream.URL
			spec.UseKeylessAccess = true
			spec.EnforcedTimeoutEnabled = true
			UpdateAPIVersion(spec, "", func(version *apidef.VersionInfo) {
				version.UseExtendedPaths = true
				version.ExtendedPaths.HardTimeouts = []apidef.HardTimeoutMeta{
					{
						Disabled: false,
						Path:     "/test2",
						Method:   http.MethodGet,
						TimeOut:  1,
					},
				}
			})
		})[0]

		ts.Gw.LoadAPI(api)

		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/test2",
			Code:      http.StatusGatewayTimeout,
			BodyMatch: "Upstream service reached hard timeout",
		})
	})

	t.Run("Basic Timeout Behavior - delay higher than both timeouts", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(3 * time.Second)
			w.Write([]byte("Success"))
		}))
		defer upstream.Close()

		api := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.Proxy.TargetURL = upstream.URL
			spec.UseKeylessAccess = true
			spec.EnforcedTimeoutEnabled = true
			UpdateAPIVersion(spec, "", func(version *apidef.VersionInfo) {
				version.UseExtendedPaths = true
				version.ExtendedPaths.HardTimeouts = []apidef.HardTimeoutMeta{
					{
						Disabled: false,
						Path:     "/test3",
						Method:   http.MethodGet,
						TimeOut:  1,
					},
				}
			})
		})[0]

		ts.Gw.LoadAPI(api)

		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/test3",
			Code:      http.StatusGatewayTimeout,
			BodyMatch: "Upstream service reached hard timeout",
		})
	})

	t.Run("Basic Timeout Behavior - delay within enforced timeout", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(1 * time.Second)
			w.Write([]byte("Success"))
		}))
		defer upstream.Close()

		api := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.Proxy.TargetURL = upstream.URL
			spec.UseKeylessAccess = true
			spec.EnforcedTimeoutEnabled = true
			UpdateAPIVersion(spec, "", func(version *apidef.VersionInfo) {
				version.UseExtendedPaths = true
				version.ExtendedPaths.HardTimeouts = []apidef.HardTimeoutMeta{
					{
						Disabled: false,
						Path:     "/test4",
						Method:   http.MethodGet,
						TimeOut:  3,
					},
				}
			})
		})[0]

		ts.Gw.LoadAPI(api)

		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/test4",
			Code:      http.StatusOK,
			BodyMatch: "Success",
		})
	})

	t.Run("Multiple Endpoints with Different Enforced Timeouts", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/delay/") {
				time.Sleep(1000 * time.Millisecond)
				w.Write([]byte("Delay 1s response"))
			} else if strings.HasPrefix(r.URL.Path, "/delay2/") {
				time.Sleep(2000 * time.Millisecond)
				w.Write([]byte("Delay2 2s response"))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer upstream.Close()

		api := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.Proxy.TargetURL = upstream.URL
			spec.UseKeylessAccess = true
			spec.EnforcedTimeoutEnabled = true
			UpdateAPIVersion(spec, "", func(version *apidef.VersionInfo) {
				version.UseExtendedPaths = true
				version.ExtendedPaths.HardTimeouts = []apidef.HardTimeoutMeta{
					{
						Disabled: false,
						Path:     "^/delay/1$",
						Method:   http.MethodGet,
						TimeOut:  4,
					},
					{
						Disabled: false,
						Path:     "^/delay2/2$",
						Method:   http.MethodGet,
						TimeOut:  1,
					},
				}
			})
		})[0]

		ts.Gw.LoadAPI(api)

		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/delay/1",
			Code:      http.StatusOK,
			BodyMatch: "Delay 1s response",
		})

		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/delay2/2",
			Code:      http.StatusGatewayTimeout,
			BodyMatch: "Upstream service reached hard timeout",
		})
	})

	t.Run("Explicit vs Default Global Timeout", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/delay/1000") {
				time.Sleep(1000 * time.Millisecond)
				w.Write([]byte("Delay 1s response"))
			} else if strings.HasPrefix(r.URL.Path, "/delay/4000") {
				time.Sleep(4000 * time.Millisecond)
				w.Write([]byte("Delay 4s response"))
			} else if strings.HasPrefix(r.URL.Path, "/delay2/1000") {
				time.Sleep(1000 * time.Millisecond)
				w.Write([]byte("Delay2 1s response"))
			} else if strings.HasPrefix(r.URL.Path, "/delay2/4000") {
				time.Sleep(4000 * time.Millisecond)
				w.Write([]byte("Delay2 4s response"))
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer upstream.Close()

		api := BuildAPI(func(spec *APISpec) {
			spec.Proxy.ListenPath = "/"
			spec.Proxy.TargetURL = upstream.URL
			spec.UseKeylessAccess = true
			spec.EnforcedTimeoutEnabled = true
			UpdateAPIVersion(spec, "", func(version *apidef.VersionInfo) {
				version.UseExtendedPaths = true
				version.ExtendedPaths.HardTimeouts = []apidef.HardTimeoutMeta{
					{
						Disabled: false,
						Path:     "/delay/.*",
						Method:   http.MethodGet,
						TimeOut:  3,
					},
					// No explicit timeout for /delay2/* endpoints - will use global
				}
			})
		})[0]

		ts.Gw.LoadAPI(api)

		// Test case 1: Should succeed (delay 45ms < enforced timeout 60ms)
		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/delay/1000",
			Code:      http.StatusOK,
			BodyMatch: "Delay 1s response",
		})

		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/delay/4000",
			Code:      http.StatusGatewayTimeout,
			BodyMatch: "Upstream service reached hard timeout",
		})

		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/delay2/1000",
			Code:      http.StatusOK,
			BodyMatch: "Delay2 1s response",
		})

		// Test case 4: Should timeout at global value (delay 60ms > global timeout 50ms)
		_, _ = ts.Run(t, test.TestCase{
			Method:    http.MethodGet,
			Path:      "/delay2/4000",
			Code:      http.StatusGatewayTimeout,
			BodyMatch: "Upstream service reached hard timeout",
		})
	})
}

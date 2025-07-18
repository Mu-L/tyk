package gateway

import (
	"fmt"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/TykTechnologies/tyk/apidef"
	"github.com/TykTechnologies/tyk/goplugin"
	"github.com/TykTechnologies/tyk/user"
)

type ResponseGoPluginMiddleware struct {
	BaseTykResponseHandler
	Path       string // path to .so file
	SymbolName string // function symbol to look up
	ResHandler func(rw http.ResponseWriter, res *http.Response, req *http.Request)
}

func (h *ResponseGoPluginMiddleware) Base() *BaseTykResponseHandler {
	return &h.BaseTykResponseHandler
}

func (h *ResponseGoPluginMiddleware) Name() string {
	return "ResponseGoPluginMiddleware"
}

func (h *ResponseGoPluginMiddleware) Init(c interface{}, spec *APISpec) error {
	h.Spec = spec
	h.Path = c.(apidef.MiddlewareDefinition).Path
	h.SymbolName = c.(apidef.MiddlewareDefinition).Name

	h.setLogger(
		h.logger().WithFields(logrus.Fields{
			"mwPath":       h.Path,
			"mwSymbolName": h.SymbolName,
		}),
	)

	if h.ResHandler != nil {
		h.logger().Info("Go-plugin middleware is already initialized")
		// noop
		return nil
	}

	newPath, err := goplugin.GetPluginFileNameToLoad(goplugin.FileSystemStorage{}, h.Path)
	if err != nil {
		h.logger().WithError(err).Error("Could not load Go-plugin. File was not found")
		return err
	}
	if h.Path != newPath {
		h.Path = newPath
	}

	// try to load plugin
	if h.ResHandler, err = goplugin.GetResponseHandler(h.Path, h.SymbolName); err != nil {
		h.logger().WithError(err).Error("Could not load Go-plugin")
		return err
	}
	h.logger().Infof("Loaded Go response plugin: %s", h.SymbolName)

	return nil
}

func (h *ResponseGoPluginMiddleware) HandleResponse(w http.ResponseWriter, res *http.Response, req *http.Request, _ *user.SessionState) error {
	return h.HandleGoPluginResponse(w, res, req)
}

func (h *ResponseGoPluginMiddleware) HandleGoPluginResponse(w http.ResponseWriter, res *http.Response, req *http.Request) error {
	// make sure tyk recover in case Go-plugin function panics
	defer func() {
		if e := recover(); e != nil {
			err := fmt.Errorf("%v", e)
			w.WriteHeader(http.StatusInternalServerError)
			h.logger().WithError(err).Error("Recovered from panic while running Go-plugin middleware func")
		}
	}()

	// Inject definition into response context
	h.Spec.injectIntoReqContext(req)

	// wrap ResponseWriter to check if response was sent
	rw := &customResponseWriter{
		ResponseWriter: w,
		copyData:       recordDetail(req, h.Spec),
	}

	// call Go-plugin function
	t1 := time.Now()

	h.ResHandler(rw, res, req)

	// calculate latency
	ms := DurationToMillisecond(time.Since(t1))
	h.logger().WithField("ms", ms).Debug("Go-plugin response processing took")

	// check if response was sent
	if rw.responseSent {
		// check if response code was an error one

		if rw.statusCodeSent >= http.StatusBadRequest {
			// base middleware will report this error to analytics if needed
			w.WriteHeader(rw.statusCodeSent)
			err := fmt.Errorf("plugin function sent error response code: %d", rw.statusCodeSent)
			h.logger().WithError(err).Error("Returned error code while processing response with Go-plugin middleware func")
			return err
		}
	}
	return nil
}

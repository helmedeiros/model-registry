package httpapi

import "net/http"

// statusRecorder wraps http.ResponseWriter to capture the response
// status code for the access log + metrics middlewares. The default
// status is http.StatusOK matching net/http's implicit behaviour when
// a handler calls Write before WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return
	}
	s.wroteHeader = true
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

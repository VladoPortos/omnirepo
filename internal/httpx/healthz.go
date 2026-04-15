package httpx

import "net/http"

// Healthz returns a cheap 200-OK liveness handler (FOUND-06). It performs no
// dependency checks; the only failure mode is the process not being able to
// serve HTTP at all (in which case clients would never reach here).
//
// Body shape is `{"status":"ok"}` — stable contract consumed by docker/k8s
// health probes and the smoke tests in internal/app.
func Healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

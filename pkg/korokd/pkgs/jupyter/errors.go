package jupyter

import "fmt"

// HTTPError represents a non-2xx response from the Jupyter server.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Body == "" {
		return fmt.Sprintf("jupyter http error: status=%d", e.Status)
	}
	return fmt.Sprintf("jupyter http error: status=%d body=%s", e.Status, e.Body)
}


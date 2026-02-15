package sender

import "fmt"

type httpStatusError struct {
	op         string
	statusCode int
	body       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: status %d, body %s", e.op, e.statusCode, e.body)
}

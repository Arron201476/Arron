package runtime

import "fmt"

type DomainError struct {
	Code    string
	Message string
}

func (e *DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func domainError(code, message string) error {
	return &DomainError{Code: code, Message: message}
}

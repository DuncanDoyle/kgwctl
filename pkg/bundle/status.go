package bundle

import (
	"errors"
	"fmt"
	"regexp"
)

// CollectorStatus is the outcome of one collector.
type CollectorStatus string

// Collector statuses.
const (
	// CollectorOK means every item was collected.
	CollectorOK CollectorStatus = "ok"
	// CollectorPartial means some items failed or were dropped.
	CollectorPartial CollectorStatus = "partial"
	// CollectorFailed means the collector returned an error or panicked.
	CollectorFailed CollectorStatus = "failed"
	// CollectorSkipped means the collector had nothing to run on.
	CollectorSkipped CollectorStatus = "skipped"
	// CollectorTimeout means the collection deadline ended the collector.
	CollectorTimeout CollectorStatus = "timeout"
)

// ItemStatus is the outcome of one item.
type ItemStatus string

// Item statuses.
const (
	// ItemOK means the item was written.
	ItemOK ItemStatus = "ok"
	// ItemSkipped means the item was not applicable or left out on purpose.
	ItemSkipped ItemStatus = "skipped"
	// ItemDenied means the API server refused access.
	ItemDenied ItemStatus = "denied"
	// ItemDisabled means the endpoint is switched off.
	ItemDisabled ItemStatus = "disabled"
	// ItemFailed means the capture failed.
	ItemFailed ItemStatus = "failed"
	// ItemTimeout means the capture timed out.
	ItemTimeout ItemStatus = "timeout"
	// ItemDropped means the writer left the item out, for example over budget.
	ItemDropped ItemStatus = "dropped"
)

// Sink sentinel errors.
var (
	// ErrSealed is returned by a Sink after the collection deadline.
	ErrSealed = errors.New("bundle: sink sealed")
	// ErrBudget is returned when an item does not fit the size budget.
	ErrBudget = errors.New("bundle: size budget exceeded")
	// ErrDuplicate is returned for a path that is already committed.
	ErrDuplicate = errors.New("bundle: duplicate path")
	// ErrInvalidPath is returned for a path or component that fails validation.
	ErrInvalidPath = errors.New("bundle: invalid path")
	// ErrForbiddenKind is returned for a kind the bundle never collects.
	ErrForbiddenKind = errors.New("bundle: forbidden kind")
)

var collectorName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName reports whether name is a valid collector name: lowercase words
// joined by single dashes.
func ValidName(name string) error {
	if !collectorName.MatchString(name) {
		return fmt.Errorf("invalid collector name %q: want lowercase letters, digits and single dashes", name)
	}
	return nil
}

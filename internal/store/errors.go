package store

import "errors"

var (
	// ErrNotFound is returned when a hash is not present in the Store.
	ErrNotFound = errors.New("store: artifact not found")

	// ErrTagUnknown is returned when a tag has never been assigned.
	ErrTagUnknown = errors.New("store: tag not assigned")

	// ErrMemberAbsent is returned when the hash exists but the requested
	// bundle member was never uploaded.
	ErrMemberAbsent = errors.New("store: bundle member absent")

	// ErrInvalidTransition is returned when a state transition is not
	// permitted by the lifecycle (e.g. tagging or activating a deprecated
	// artifact, or re-deprecating an already deprecated one).
	ErrInvalidTransition = errors.New("store: invalid state transition")

	// ErrCorrupt is returned when the backing's on-disk state and index
	// disagree on what exists.
	ErrCorrupt = errors.New("store: storage corrupted")

	// ErrSourceRequired is returned by PutRequest.Validate when
	// SourceBytes is empty.
	ErrSourceRequired = errors.New("store: source bytes required")

	// ErrContentTypeRequired is returned by PutRequest.Validate when
	// ContentType is empty.
	ErrContentTypeRequired = errors.New("store: content type required")
)

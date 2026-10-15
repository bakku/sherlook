package graph

import (
	"fmt"
	"time"
)

// EmailAddress is a Graph emailAddress resource.
type EmailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Recipient is a Graph recipient resource.
type Recipient struct {
	EmailAddress EmailAddress `json:"emailAddress"`
}

// String renders the recipient as `Name <address>`, or just one of them if the other is missing.
func (r Recipient) String() string {
	name, addr := r.EmailAddress.Name, r.EmailAddress.Address
	switch {
	case name == "" || name == addr:
		return addr
	case addr == "":
		return name
	default:
		return fmt.Sprintf("%s <%s>", name, addr)
	}
}

// ShortName is the display name if present, otherwise the address.
func (r Recipient) ShortName() string {
	if r.EmailAddress.Name != "" {
		return r.EmailAddress.Name
	}
	return r.EmailAddress.Address
}

// MessageSummary holds the fields needed to render the message list.
type MessageSummary struct {
	ID               string     `json:"id"`
	Subject          string     `json:"subject"`
	From             *Recipient `json:"from"`
	ReceivedDateTime time.Time  `json:"receivedDateTime"`
	IsRead           bool       `json:"isRead"`
	HasAttachments   bool       `json:"hasAttachments"`
}

// ItemBody is a Graph itemBody resource. ContentType is "html" or "text".
type ItemBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

// Message is a full message as shown in the preview.
type Message struct {
	MessageSummary
	ToRecipients []Recipient  `json:"toRecipients"`
	CcRecipients []Recipient  `json:"ccRecipients"`
	Body         ItemBody     `json:"body"`
	Attachments  []Attachment `json:"-"`
}

// Attachment holds attachment metadata only; content is never fetched.
type Attachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	IsInline    bool   `json:"isInline"`
}

// Page is one page of the inbox listing.
type Page struct {
	Messages []MessageSummary
	NextLink string
}

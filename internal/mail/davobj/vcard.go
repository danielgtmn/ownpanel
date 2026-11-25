package davobj

import (
	"bytes"
	"errors"
	"io"

	"github.com/emersion/go-vcard"
)

// DecodeCard parses exactly one vCard.
func DecodeCard(data []byte) (vcard.Card, error) {
	if err := checkText(data, ValidAddressData); err != nil {
		return nil, err
	}
	dec := vcard.NewDecoder(bytes.NewReader(data))
	card, err := dec.Decode()
	if err != nil {
		return nil, invalid(ValidAddressData, "the contact cannot be read: %v", err)
	}
	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		return nil, invalid(ValidAddressData, "one contact is stored per resource")
	}
	return card, nil
}

// ParseCard checks an address object resource (RFC 6352 section 5.1): one
// vCard with one UID.
func ParseCard(data []byte) (Info, vcard.Card, error) {
	if len(data) > MaxObjectBytes {
		return Info{}, nil, invalid(MaxResourceSize, "the contact is larger than %d bytes", MaxObjectBytes)
	}
	card, err := DecodeCard(data)
	if err != nil {
		return Info{}, nil, err
	}
	uids := card[vcard.FieldUID]
	if len(uids) != 1 || !ValidUID(uids[0].Value) {
		return Info{}, nil, invalid(ValidAddressData, "a contact needs exactly one UID")
	}
	return Info{UID: uids[0].Value, Component: CompCard, Start: NoStart, End: NoEnd}, card, nil
}

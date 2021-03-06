package bertymessenger

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/eknkc/basex"
	"github.com/gogo/protobuf/proto"
	"github.com/mr-tron/base58"

	"berty.tech/berty/v2/go/pkg/bertytypes"
	"berty.tech/berty/v2/go/pkg/errcode"
)

// Marshal returns shareable web and internal URLs.
//
// The web URL is meant to:
// - be short,
// - have some parts that are human-readable,
// - to point to a sub-page of the berty.tech website where some JS code will parse the human-readable part.
//
// The internal URL is meant to generate the most tiny QR codes. These QR codes can only be opened by a Berty app.
//
// Marshal will return an error if the provided link does not contain all the mandatory fields;
// it may also filter-out some sensitive data.
func (link *BertyLink) Marshal() (internal string, web string, err error) {
	if link == nil || link.Kind == BertyLink_UnknownKind {
		return "", "", errcode.ErrMissingInput
	}

	if err := link.IsValid(); err != nil {
		return "", "", err
	}

	var (
		// web
		kind    string
		machine = &BertyLink{}
		human   = url.Values{}

		// internal
		qrOptimized = &BertyLink{}
	)

	switch link.Kind {
	case BertyLink_ContactInviteV1Kind:
		kind = "contact"
		machine.BertyID = &BertyID{
			PublicRendezvousSeed: link.BertyID.PublicRendezvousSeed,
			AccountPK:            link.BertyID.AccountPK,
		}
		if link.BertyID.DisplayName != "" {
			human.Add("name", link.BertyID.DisplayName)
		}

		// for contact sharing, there are no fields to hide, so just copy the input link
		*qrOptimized = *link
	case BertyLink_GroupV1Kind:
		kind = "group"
		machine.BertyGroup = &BertyGroup{
			Group: &bertytypes.Group{
				PublicKey: link.BertyGroup.Group.PublicKey,
				Secret:    link.BertyGroup.Group.Secret,
				SecretSig: link.BertyGroup.Group.SecretSig,
				GroupType: link.BertyGroup.Group.GroupType,
				SignPub:   link.BertyGroup.Group.SignPub,
			},
		}
		if link.BertyGroup.DisplayName != "" {
			human.Add("name", link.BertyGroup.DisplayName)
		}
		*qrOptimized = *link
	default:
		return "", "", errcode.ErrInvalidInput
	}

	// compute the web shareable link.
	// in this mode, we have:
	// - a human-readable link kind
	// - a base58-encoded binary (proto) representation of the link (without the kind and metadata)
	// - human-readable metadata, encoded as query string (including display name)
	{
		machineBin, err := proto.Marshal(machine)
		if err != nil {
			return "", "", errcode.ErrInvalidInput.Wrap(err)
		}
		// here we use base58 which is compressed enough whilst being easy to read by a human.
		// another candidate could be base58.RawURLEncoding which is a little bit more compressed and also only containing unescaped URL chars.
		machineEncoded := base58.Encode(machineBin)
		path := kind + "/" + machineEncoded
		if len(human) > 0 {
			path += "/" + human.Encode()
		}
		// we use a '#' to improve privacy by preventing the webservers to get aware of the right part of this URL
		web = LinkWebPrefix + path
	}

	// compute the internal shareable link.
	// in this mode, the url is as short as possible, in the format: berty://{base45(proto.marshal(link))}.
	{
		qrBin, err := proto.Marshal(qrOptimized)
		if err != nil {
			return "", "", errcode.ErrInvalidInput.Wrap(err)
		}
		// using uppercase to stay in the QR AlphaNum's 45chars alphabet
		internal = LinkInternalPrefix + "PB/" + qrBaseEncoder.Encode(qrBin)
	}

	return internal, web, nil
}

// UnmarshalLink takes an URL generated by BertyLink.Marshal (or manually crafted), and returns a BertyLink object.
func UnmarshalLink(uri string) (*BertyLink, error) {
	if uri == "" {
		return nil, errcode.ErrMissingInput
	}

	// internal format
	if strings.HasPrefix(strings.ToLower(uri), strings.ToLower(LinkInternalPrefix)) {
		right := uri[len(LinkInternalPrefix):]
		parts := strings.Split(right, "/")
		if len(parts) < 2 {
			return nil, errcode.ErrInvalidInput.Wrap(fmt.Errorf("URI should have at least 2 parts"))
		}
		switch strings.ToLower(parts[0]) {
		case "pb":
			blob := strings.Join(parts[1:], "/")
			qrBin, err := qrBaseEncoder.Decode(blob)
			if err != nil {
				return nil, errcode.ErrInvalidInput.Wrap(err)
			}
			var link BertyLink
			err = proto.Unmarshal(qrBin, &link)
			if err != nil {
				return nil, errcode.ErrInvalidInput.Wrap(err)
			}
			return &link, nil
		default:
			return nil, errcode.ErrInvalidInput.Wrap(fmt.Errorf("unsupported link type: %q", parts[0]))
		}
	}

	// web format
	if strings.HasPrefix(strings.ToLower(uri), strings.ToLower(LinkWebPrefix)) {
		parsed, err := url.Parse(uri)
		if err != nil {
			return nil, errcode.ErrInvalidInput.Wrap(err)
		}
		if parsed.Fragment == "" {
			return nil, errcode.ErrInvalidInput.Wrap(err)
		}

		rawFragment := strings.Join(strings.Split(uri, "#")[1:], "#") // required by go1.14
		// when minimal version of berty will be go1.15, we can just use `parsed.EscapedFragment()`

		link := BertyLink{}
		parts := strings.Split(rawFragment, "/")
		if len(parts) < 2 {
			return nil, errcode.ErrInvalidInput.Wrap(fmt.Errorf("URI should have at least 2 parts"))
		}

		// decode blob
		machineBin, err := base58.Decode(parts[1])
		if err != nil {
			return nil, errcode.ErrInvalidInput.Wrap(err)
		}
		if err := proto.Unmarshal(machineBin, &link); err != nil {
			return nil, errcode.ErrInvalidInput.Wrap(err)
		}

		// decode url.Values
		var human url.Values
		if len(parts) > 2 {
			encodedValues := strings.Join(parts[2:], "/")
			human, err = url.ParseQuery(encodedValues)
			if err != nil {
				return nil, errcode.ErrInvalidInput.Wrap(err)
			}
		}

		// per-kind merging strategies and checks
		switch kind := parts[0]; kind {
		case "contact":
			link.Kind = BertyLink_ContactInviteV1Kind
			if link.BertyID == nil {
				link.BertyID = &BertyID{}
			}
			if name := human.Get("name"); name != "" && link.BertyID.DisplayName == "" {
				link.BertyID.DisplayName = name
			}
		case "group":
			link.Kind = BertyLink_GroupV1Kind
			if link.BertyGroup == nil {
				link.BertyGroup = &BertyGroup{}
			}
			if name := human.Get("name"); name != "" && link.BertyGroup.DisplayName == "" {
				link.BertyGroup.DisplayName = name
			}
		default:
			return nil, errcode.ErrInvalidInput
		}

		return &link, nil
	}

	return nil, errcode.ErrInvalidInput.Wrap(fmt.Errorf("unsupported link format"))
}

const (
	LinkWebPrefix      = "https://berty.tech/id#"
	LinkInternalPrefix = "BERTY://"
)

// from https://www.swisseduc.ch/informatik/theoretische_informatik/qr_codes/docs/qr_standard.pdf
//
// Alphanumeric Mode encodes data from a set of 45 characters, i.e.
// - 10 numeric digits (0 - 9) (ASCII values 30 to 39),
// - 26 alphabetic characters (A - Z) (ASCII values 41 to 5A),
// - and 9 symbols (SP, $, %, *, +, -, ., /, :) (ASCII values 20, 24, 25, 2A, 2B, 2D to 2F, 3A).
//
// we remove SP, %, +, which changes when passed through url.Encode.
//
// the generated string is longer than a base58 one, but the generated QR code is smaller which is best for scanning.
var qrBaseEncoder, _ = basex.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789$*-.:/")

func (link *BertyLink) IsContact() bool {
	return link.Kind == BertyLink_ContactInviteV1Kind &&
		link.IsValid() == nil
}

func (link *BertyLink) IsGroup() bool {
	return link.Kind == BertyLink_GroupV1Kind &&
		link.IsValid() == nil
}

func (link *BertyLink) IsValid() error {
	if link == nil {
		return errcode.ErrMissingInput
	}
	switch link.Kind {
	case BertyLink_ContactInviteV1Kind:
		if link.BertyID == nil ||
			link.BertyID.AccountPK == nil ||
			link.BertyID.PublicRendezvousSeed == nil {
			return errcode.ErrMissingInput
		}
		return nil
	case BertyLink_GroupV1Kind:
		if link.BertyGroup == nil {
			return errcode.ErrMissingInput
		}
		if groupType := link.BertyGroup.Group.GroupType; groupType != bertytypes.GroupTypeMultiMember {
			return errcode.ErrInvalidInput.Wrap(fmt.Errorf("can't share a %q group type", groupType))
		}
		return nil
	}
	return errcode.ErrInvalidInput
}

func (id *BertyID) GetBertyLink() *BertyLink {
	return &BertyLink{
		Kind:    BertyLink_ContactInviteV1Kind,
		BertyID: id,
	}
}

func (group *BertyGroup) GetBertyLink() *BertyLink {
	return &BertyLink{
		Kind:       BertyLink_GroupV1Kind,
		BertyGroup: group,
	}
}

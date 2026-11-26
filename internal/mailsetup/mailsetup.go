// Package mailsetup describes how mail programs connect to the mail
// service and builds the setup profile for Apple devices. The panel (for
// the owners of mailboxes) and the mail service's self-service area (for
// the mailbox itself) offer the same settings and the same profile.
package mailsetup

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// ClientSettings is what a mail program needs to connect.
type ClientSettings struct {
	Server          string `json:"server"`
	IMAPPort        int    `json:"imap_port"`
	SMTPPort        int    `json:"smtp_port"`
	SMTPStartTLS    int    `json:"smtp_starttls_port"`
	UserNameIsEmail bool   `json:"user_name_is_address"`
}

// Settings are the settings for mail programs of the mail service on host:
// IMAP over TLS on 993, sending over TLS on 465 or with STARTTLS on 587,
// the whole address as user name.
func Settings(host string) ClientSettings {
	return ClientSettings{Server: host, IMAPPort: 993, SMTPPort: 465, SMTPStartTLS: 587, UserNameIsEmail: true}
}

// profileUUID is a stable UUID (version 5 layout) for a part of the
// profile of address, so downloading it again replaces the installed one.
func profileUUID(part, address, host string) string {
	h := sha256.Sum256([]byte("ownpanel mail profile\x00" + part + "\x00" + address + "\x00" + host))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// plist writes an Apple property list of dictionaries, strings, integers
// and booleans; every string is escaped.
type plist struct{ bytes.Buffer }

func (p *plist) key(k string) {
	p.WriteString("<key>")
	_ = xml.EscapeText(p, []byte(k))
	p.WriteString("</key>")
}

func (p *plist) str(k, v string) {
	p.key(k)
	p.WriteString("<string>")
	_ = xml.EscapeText(p, []byte(v))
	p.WriteString("</string>\n")
}

func (p *plist) integer(k string, v int) {
	p.key(k)
	p.WriteString("<integer>" + strconv.Itoa(v) + "</integer>\n")
}

func (p *plist) boolean(k string, v bool) {
	p.key(k)
	if v {
		p.WriteString("<true/>\n")
	} else {
		p.WriteString("<false/>\n")
	}
}

// ProfileContentType is the media type of AppleProfile.
const ProfileContentType = "application/x-apple-aspen-config"

// AppleProfile is an unsigned configuration profile with one mail account
// (com.apple.mail.managed) for address; the device asks for the password
// when the profile is installed. It never contains a password.
func AppleProfile(address string, c ClientSettings) []byte {
	addr := address
	var p plist
	p.WriteString(xml.Header)
	p.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	p.WriteString("<plist version=\"1.0\">\n<dict>\n")
	p.key("PayloadContent")
	p.WriteString("\n<array>\n<dict>\n")
	p.str("EmailAccountType", "EmailTypeIMAP")
	p.str("EmailAccountDescription", addr)
	p.str("EmailAddress", addr)
	p.str("IncomingMailServerHostName", c.Server)
	p.integer("IncomingMailServerPortNumber", c.IMAPPort)
	p.boolean("IncomingMailServerUseSSL", true)
	p.str("IncomingMailServerUsername", addr)
	p.str("IncomingMailServerAuthentication", "EmailAuthPassword")
	p.str("OutgoingMailServerHostName", c.Server)
	p.integer("OutgoingMailServerPortNumber", c.SMTPPort)
	p.boolean("OutgoingMailServerUseSSL", true)
	p.str("OutgoingMailServerUsername", addr)
	p.str("OutgoingMailServerAuthentication", "EmailAuthPassword")
	p.boolean("OutgoingPasswordSameAsIncomingPassword", true)
	p.str("PayloadDescription", "Mail account "+addr)
	p.str("PayloadDisplayName", addr)
	p.str("PayloadIdentifier", "ownpanel.mail."+profileUUID("account", addr, c.Server))
	p.str("PayloadType", "com.apple.mail.managed")
	p.str("PayloadUUID", profileUUID("account", addr, c.Server))
	p.integer("PayloadVersion", 1)
	p.WriteString("</dict>\n</array>\n")
	p.str("PayloadDescription", "Sets up the mail account "+addr+". The password is asked for when the profile is installed.")
	p.str("PayloadDisplayName", "Mail: "+addr)
	p.str("PayloadIdentifier", "ownpanel.mail.profile."+profileUUID("profile", addr, c.Server))
	p.boolean("PayloadRemovalDisallowed", false)
	p.str("PayloadType", "Configuration")
	p.str("PayloadUUID", profileUUID("profile", addr, c.Server))
	p.integer("PayloadVersion", 1)
	p.WriteString("</dict>\n</plist>\n")
	return p.Bytes()
}

// ProfileFileName is the file name of the profile of address: the
// characters of the address that are safe in a file name everywhere.
func ProfileFileName(address string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '@', r == '-', r == '_':
			return r
		}
		return '-'
	}, address)
	return name + ".mobileconfig"
}

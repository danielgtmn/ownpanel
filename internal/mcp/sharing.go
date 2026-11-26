package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// Shared mailboxes and access to them (G3). Making a shared mailbox
// personal again needs a password, which never passes through an AI agent:
// that is done in the web interface. People share their own personal
// mailbox themselves, in their own settings; here access can only be taken
// away from it.

type setMailboxKindInput struct {
	ID           string `json:"id" jsonschema:"mailbox id from list_mailboxes"`
	Kind         string `json:"kind,omitempty" jsonschema:"shared turns a personal mailbox into a shared one: its password and app passwords are removed and everyone signed in to it is signed out; people then use it with their own mailbox. Leave empty to keep the kind. Making it personal again needs a password and is done in the web interface"`
	KeepSentCopy *bool  `json:"keep_sent_copy,omitempty" jsonschema:"shared mailboxes only: true keeps a copy of the mail sent for it in its Sent folder; leave out to keep the setting"`
}

type setMailboxAccessInput struct {
	ID      string   `json:"id" jsonschema:"id of a shared mailbox from list_mailboxes"`
	Address string   `json:"address" jsonschema:"address of a personal mailbox of the same organisation that gets access"`
	Rights  []string `json:"rights" jsonschema:"what it may do: read (folders and messages, with its own read state), write (flags, moving, deleting, folders; includes read), manage (filters and out-of-office of the shared mailbox), send-as (send with the shared address as sender), send-on-behalf (send for the shared address, showing who sent it)"`
}

type removeMailboxAccessInput struct {
	ID        string `json:"id" jsonschema:"id of the mailbox from list_mailboxes"`
	GranteeID string `json:"grantee_id" jsonschema:"mailbox_id of the mailbox losing access, from shared_with"`
}

func addSharingTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "set_mailbox_kind",
		Title: "Make a mailbox shared",
		Description: "Turn a personal mailbox into a shared mailbox (for example info@ or support@ that several people read with " +
			"their own sign-in), and choose whether a shared mailbox keeps a copy of the mail sent for it. Making a mailbox " +
			"shared removes its password and app passwords and signs out everyone using it; only call it when the user asked " +
			"for it. Returns the mailbox.",
		Annotations: annotations("Make a mailbox shared", false, true, true),
	}, func(ctx context.Context, c call, in setMailboxKindInput) (mailboxOutput, error) {
		var m panel.Mailbox
		var err error
		if in.Kind != "" {
			if in.Kind != "shared" {
				return mailboxOutput{}, plainError("only kind shared can be set here: making a mailbox personal needs a password, which is set in the web interface")
			}
			if m, err = p.SetMailboxKind(ctx, c.actor, in.ID, in.Kind, ""); err != nil && panel.KindOf(err) != panel.NotSynced {
				return mailboxOutput{}, err
			}
		}
		if in.KeepSentCopy != nil {
			m, err = p.SetMailboxKeepSentCopy(ctx, c.actor, in.ID, *in.KeepSentCopy)
		} else if in.Kind == "" {
			m, err = p.MailboxOf(ctx, c.actor, in.ID)
		}
		return mailboxOutput{mailboxInfoOf(m)}, err
	})

	add(s, &sdk.Tool{
		Name:  "set_mailbox_access",
		Title: "Give access to a shared mailbox",
		Description: "Give a personal mailbox of the same organisation access to a shared mailbox, or change what it may do. " +
			"Its owner then sees the shared mailbox's folders under Shared/<address> in their mail program and, with send-as " +
			"or send-on-behalf, can send for it. Returns the shared mailbox with everyone who has access.",
		Annotations: annotations("Give access to a shared mailbox", false, false, true),
	}, func(ctx context.Context, c call, in setMailboxAccessInput) (mailboxOutput, error) {
		m, err := p.SetMailboxGrant(ctx, c.actor, in.ID, in.Address, in.Rights)
		return mailboxOutput{mailboxInfoOf(m)}, err
	})

	add(s, &sdk.Tool{
		Name:  "remove_mailbox_access",
		Title: "Take access to a mailbox away",
		Description: "Take away a mailbox's access to a shared mailbox, or to a personal mailbox its owner shared. Mail programs " +
			"that have one of its folders open are disconnected. Returns the mailbox.",
		Annotations: annotations("Take access to a mailbox away", false, true, true),
	}, func(ctx context.Context, c call, in removeMailboxAccessInput) (mailboxOutput, error) {
		m, err := p.RemoveMailboxGrant(ctx, c.actor, in.ID, in.GranteeID)
		return mailboxOutput{mailboxInfoOf(m)}, err
	})
}

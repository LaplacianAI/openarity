package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"rsc.io/qr"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

// How pairing is followed: the brain is asked every two seconds, since a QR
// changes about every 20; a pairing is given five minutes; a channel no role
// has taken within half a minute has no role to take it. Variables so a test
// does not wait for them.
var (
	pairPoll   = 2 * time.Second
	pairWait   = 5 * time.Minute
	pairNoRole = 30 * time.Second
)

// pairingView is one state of a pairing, as -o json and yaml print it.
type pairingView struct {
	State  string `json:"state" yaml:"state"`
	Method string `json:"method,omitempty" yaml:"method,omitempty"`
	QR     string `json:"qr,omitempty" yaml:"qr,omitempty"`
	Code   string `json:"code,omitempty" yaml:"code,omitempty"`
	Phone  string `json:"phone,omitempty" yaml:"phone,omitempty"`
	Error  string `json:"error,omitempty" yaml:"error,omitempty"`
	Held   bool   `json:"held" yaml:"held"`
}

func viewOf(p *client.ChannelPairing) pairingView {
	v := pairingView{State: string(p.State), Held: p.Held}
	if p.Method != nil {
		v.Method = string(*p.Method)
	}
	v.QR, v.Code, v.Phone, v.Error = deref(p.Qr), deref(p.Code), deref(p.Phone), deref(p.Error)
	return v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func newPairCmd(opts *cli.Options) *cobra.Command {
	var byCode, invert bool
	var phone string

	cmd := &cobra.Command{
		Use:   "pair <team> <channel>",
		Short: "Link a WhatsApp number to a channel, by QR code or by a pairing code",
		Long: "Both arguments take a name or an id. Needs channel:write in the team.\n\n" +
			"With no flags a QR code is drawn here; scan it from WhatsApp on the\n" +
			"phone: Settings, Linked devices, Link a device. It changes about every\n" +
			"20 seconds, and the new one is drawn. With --code --phone, WhatsApp\n" +
			"gives a code for that number instead, to enter on the phone after\n" +
			"\"Link with phone number\".\n\n" +
			"It waits until the number is linked, up to five minutes. Pairing a\n" +
			"channel already linked replaces its number. The brain's deployment must\n" +
			"run WhatsApp's role; without it nothing pairs.\n\n" +
			"The QR is drawn for a dark terminal; --invert draws it for a light one.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := client.PairChannelJSONRequestBody{Method: client.PairChannelRequestMethodQr}
			switch {
			case byCode && strings.TrimSpace(phone) == "":
				return errors.New("--code needs --phone: the number to link, with its country code, such as +91 98765 43210")
			case !byCode && cmd.Flags().Changed("phone"):
				return errors.New("--phone is only for --code; a QR code needs no number")
			case byCode:
				p := strings.TrimSpace(phone)
				body.Method, body.Phone = client.PairChannelRequestMethodCode, &p
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			channel, err := cli.ResolveChannel(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}
			p, err := cli.Accepted(api.PairChannelWithResponse(cmd.Context(), team, channel, body))
			if err != nil {
				return cli.Needs(err, "channel:write")
			}

			f := follower{opts: opts, invert: invert, started: time.Now()}
			ctx, cancel := context.WithTimeout(cmd.Context(), pairWait)
			defer cancel()
			for {
				done, err := f.show(viewOf(p))
				if done || err != nil {
					return err
				}
				err = wait(ctx, pairPoll)
				if err == nil {
					p, err = cli.Result(api.GetChannelPairingWithResponse(ctx, team, channel))
				}
				// The wait can end between asks or during one.
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return fmt.Errorf("not linked within %s; run it again for a new code", pairWait)
				}
				if err != nil {
					return cli.Needs(err, "channel:write")
				}
			}
		},
	}

	cmd.Flags().BoolVar(&byCode, "code", false, "a pairing code to enter on the phone, instead of a QR code")
	cmd.Flags().StringVar(&phone, "phone", "", "with --code: the number to link, with its country code")
	cmd.Flags().BoolVar(&invert, "invert", false, "draw the QR code for a terminal with a light background")
	return cmd
}

// follower prints a pairing as it changes: each new QR or code once, and
// how it ended.
type follower struct {
	opts    *cli.Options
	invert  bool
	started time.Time
	last    pairingView
	shown   bool
}

func (f *follower) show(v pairingView) (bool, error) {
	changed := !f.shown || v != f.last
	f.last, f.shown = v, true

	switch {
	case v.State == string(client.Connected):
		return true, f.opts.Out.Line(v, printer.Options{Table: func(table *printer.Table) {
			table.Row(f.opts.Styles.OK.Render("linked"), f.opts.Styles.Value.Render("+"+cli.Safe(v.Phone)))
		}})
	case v.State == string(client.Unpaired) && v.Error != "":
		if err := f.opts.Out.Line(v, printer.Options{Table: func(*printer.Table) {}}); err != nil {
			return true, err
		}
		return true, fmt.Errorf("pairing stopped: %s", cli.Line(v.Error))
	case !v.Held && time.Since(f.started) > pairNoRole:
		return true, errors.New("no WhatsApp role has taken this channel: the deployment needs WHATSAPP_ENABLED=1, and the role running")
	case !changed:
		return false, nil
	}

	if err := f.opts.Out.Line(v, printer.Options{Table: func(table *printer.Table) {
		switch {
		case v.Code != "":
			table.Row(f.opts.Styles.Muted.Render("code"), f.opts.Styles.Value.Render(cli.Safe(v.Code)),
				f.opts.Styles.Muted.Render("enter it on the phone: Linked devices, Link a device, Link with phone number"))
		case v.QR != "":
			table.Row(f.opts.Styles.Muted.Render("scan it from WhatsApp: Settings, Linked devices, Link a device"))
		default:
			table.Row(f.opts.Styles.Muted.Render("waiting for WhatsApp…"))
		}
	}}); err != nil {
		return true, err
	}
	if v.QR != "" {
		drawn, err := drawQR(v.QR, f.invert)
		if err != nil {
			return true, err
		}
		f.opts.Out.Note(drawn)
	}
	return false, nil
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// quiet is the margin a scanner needs around a QR code, in modules.
const quiet = 2

// drawQR draws text as a QR code in block characters, two rows of modules to
// a line. Light modules are drawn and dark ones left as the background, which
// is right on a dark terminal; invert is for a light one. Only the code's
// modules reach the terminal, never the text itself.
func drawQR(text string, invert bool) (string, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return "", fmt.Errorf("drawing the QR code: %w", err)
	}
	light := func(x, y int) bool {
		in := x >= 0 && y >= 0 && x < code.Size && y < code.Size
		dark := in && code.Black(x, y)
		return dark == invert
	}
	var b strings.Builder
	for y := -quiet; y < code.Size+quiet; y += 2 {
		for x := -quiet; x < code.Size+quiet; x++ {
			top, bottom := light(x, y), light(x, y+1)
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		b.WriteRune('\n')
	}
	return b.String(), nil
}

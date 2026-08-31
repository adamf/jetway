// Command jetwayctl is the operator's command line for Jetway.
//
// The decode subcommand works entirely offline. That is deliberate: the most
// common thing an integration engineer needs is to point a tool at a captured
// message and be told what it says and what is wrong with it, without standing
// anything up first.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/adamf/jetway/pkg/api"
	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/store"
)

const usage = `jetwayctl — operate and inspect a Jetway gateway

Usage:
  jetwayctl decode <file|->            decode a captured message and explain it
  jetwayctl book <carrier> <flight> <class> <board> <off> [date] [surname/given]
  jetwayctl pnr <locator>              show a record, its itinerary and its history
  jetwayctl pnrs                       list records
  jetwayctl messages [limit]           list recent traffic
  jetwayctl show <message-id>          show one message, raw and decoded
  jetwayctl replay <message-id>        reprocess a stored inbound message
  jetwayctl status                     show identity and link state
  jetwayctl schema                     print the database schema

Environment:
  JETWAY_URL   gateway base URL (default http://127.0.0.1:8080)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "decode":
		err = cmdDecode(os.Args[2:])
	case "book":
		err = cmdBook(os.Args[2:])
	case "pnr":
		err = cmdPNR(os.Args[2:])
	case "pnrs":
		err = cmdPNRs()
	case "messages":
		err = cmdMessages(os.Args[2:])
	case "show":
		err = cmdShow(os.Args[2:])
	case "replay":
		err = cmdReplay(os.Args[2:])
	case "status":
		err = cmdStatus()
	case "schema":
		err = cmdSchema()
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jetwayctl:", err)
		os.Exit(1)
	}
}

func base() string {
	if v := os.Getenv("JETWAY_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8080"
}

func get(path string, out any) error {
	resp, err := http.Get(base() + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s: %s", path, e.Error)
		}
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.Unmarshal(body, out)
}

func post(path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := http.Post(base()+path, "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func cmdDecode(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("decode needs one file, or - for stdin")
	}
	var raw []byte
	var err error
	if args[0] == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(args[0])
	}
	if err != nil {
		return err
	}
	printExplained(api.Explain(raw))
	return nil
}

func printExplained(e *api.Explained) {
	fmt.Printf("%s — %s\n\n", e.Format, e.Summary)
	if len(e.Envelope) > 0 {
		fmt.Println("Envelope")
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, f := range e.Envelope {
			note := ""
			if f.Note != "" {
				note = "  (" + f.Note + ")"
			}
			fmt.Fprintf(tw, "  %s\t%s%s\n", f.Name, f.Value, note)
		}
		tw.Flush()
		fmt.Println()
	}
	if len(e.Parts) > 0 {
		fmt.Println("Body")
		for _, p := range e.Parts {
			mark := " "
			if p.Unrecognised {
				mark = "?"
			}
			fmt.Printf("  %s %-8s %s\n", mark, p.Kind, p.Wire)
			for _, f := range p.Fields {
				note := ""
				if f.Note != "" {
					note = "   " + f.Note
				}
				fmt.Printf("      %-14s %s%s\n", f.Name, f.Value, note)
			}
			if p.Note != "" {
				fmt.Printf("      %s\n", p.Note)
			}
		}
		fmt.Println()
	}
	if len(e.Diagnostics) > 0 {
		fmt.Println("Diagnostics")
		for _, d := range e.Diagnostics {
			fmt.Printf("  %-5s %s/%s: %s\n", d.Severity, d.Layer, d.Code, d.Detail)
		}
		fmt.Println()
	}
}

func cmdBook(args []string) error {
	if len(args) < 5 {
		return fmt.Errorf("book needs <carrier> <flight> <class> <board> <off> [date] [surname/given]")
	}
	date := pnr.FormatDate(time.Now().UTC().AddDate(0, 0, 30))
	if len(args) >= 6 {
		date = strings.ToUpper(args[5])
	}
	surname, given := "SMITH", "JOHN"
	if len(args) >= 7 {
		if s, g, ok := strings.Cut(args[6], "/"); ok {
			surname, given = strings.ToUpper(s), strings.ToUpper(g)
		}
	}
	req := map[string]any{
		"passengers": []map[string]any{{"surname": surname, "given": given, "title": "MR"}},
		"segments": []map[string]any{{
			"carrier": strings.ToUpper(args[0]), "flight_num": args[1],
			"class": strings.ToUpper(args[2]), "date": date,
			"board": strings.ToUpper(args[3]), "off": strings.ToUpper(args[4]), "seats": 1,
		}},
		"agent": "CTL",
	}
	var out struct {
		PNR      *pnr.PNR `json:"pnr"`
		Sent     []string `json:"sent"`
		Carriers []string `json:"carriers"`
	}
	if err := post("/api/book", req, &out); err != nil {
		return err
	}
	fmt.Printf("created %s  requested from %s (%d message(s) sent)\n",
		out.PNR.RecordLocator, strings.Join(out.Carriers, ", "), len(out.Sent))
	fmt.Println("the reply arrives asynchronously; run: jetwayctl pnr " + out.PNR.RecordLocator)
	return nil
}

func cmdPNR(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("pnr needs a record locator")
	}
	var out struct {
		PNR    *pnr.PNR `json:"pnr"`
		Events []struct {
			Seq       int64     `json:"seq"`
			Type      string    `json:"type"`
			Detail    string    `json:"detail"`
			Actor     string    `json:"actor"`
			MessageID string    `json:"message_id"`
			At        time.Time `json:"at"`
		} `json:"events"`
	}
	if err := get("/api/pnr/"+url.PathEscape(strings.ToUpper(args[0])), &out); err != nil {
		return err
	}
	p := out.PNR
	fmt.Printf("%s  v%d  %s\n", p.RecordLocator, p.Version, p.Status)
	for _, x := range p.Passengers {
		fmt.Printf("  %d  %s\n", x.Ref, x.Display())
	}
	for _, s := range p.Segments {
		fmt.Printf("  %d  %s\n", s.Ref, s.Describe())
	}
	for _, s := range p.SSRs {
		fmt.Printf("  SSR %s %s %s\n", s.Code, s.Status, s.Text)
	}
	for _, l := range p.Locators {
		fmt.Printf("  locator %s %s\n", l.Owner, l.Value)
	}
	for _, f := range p.Unparsed {
		fmt.Printf("  unparsed [%s] %s\n", f.Detail, f.Raw)
	}
	fmt.Println("\nHistory")
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, e := range out.Events {
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", e.Seq, e.At.Format("15:04:05"),
			e.Actor, e.Type, e.Detail)
	}
	return tw.Flush()
}

func cmdPNRs() error {
	var out struct {
		PNRs []*pnr.PNR `json:"pnrs"`
	}
	if err := get("/api/pnrs?limit=100", &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "LOCATOR\tV\tSTATUS\tITINERARY")
	for _, p := range out.PNRs {
		segs := make([]string, 0, len(p.Segments))
		for _, s := range p.Segments {
			segs = append(segs, s.Describe())
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", p.RecordLocator, p.Version, p.Status, strings.Join(segs, "; "))
	}
	return tw.Flush()
}

type msgRow struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"`
	At        time.Time `json:"at"`
	Peer      string    `json:"peer"`
	Format    string    `json:"format"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Size      int       `json:"size"`
	Error     string    `json:"error"`
}

func cmdMessages(args []string) error {
	limit := "50"
	if len(args) > 0 {
		limit = args[0]
	}
	var out struct {
		Messages []msgRow `json:"messages"`
	}
	if err := get("/api/messages?limit="+url.QueryEscape(limit), &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tDIR\tPEER\tKIND\tSTATUS\tSIZE\tID")
	for _, m := range out.Messages {
		note := m.Status
		if m.Error != "" {
			note += " (" + m.Error + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%dB\t%s\n",
			m.At.Format("15:04:05"), m.Direction, m.Peer, m.Kind, note, m.Size, m.ID)
	}
	return tw.Flush()
}

func cmdShow(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("show needs a message id")
	}
	var out struct {
		Raw       string         `json:"raw"`
		Explained *api.Explained `json:"explained"`
	}
	if err := get("/api/message/"+url.PathEscape(args[0]), &out); err != nil {
		return err
	}
	fmt.Println("Raw wire bytes")
	for _, l := range strings.Split(strings.ReplaceAll(out.Raw, "\r\n", "\n"), "\n") {
		fmt.Println("  " + l)
	}
	fmt.Println()
	printExplained(out.Explained)
	return nil
}

func cmdReplay(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("replay needs a message id")
	}
	var out map[string]any
	if err := post("/api/message/"+url.PathEscape(args[0])+"/replay", nil, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

// cmdSchema prints the embedded schema, for bootstrapping a database by hand
// or for review by a DBA who will not run an unknown binary against production.
func cmdSchema() error {
	sql, err := store.SchemaSQL()
	if err != nil {
		return err
	}
	fmt.Print(sql)
	return nil
}

func cmdStatus() error {
	var out struct {
		Identity map[string]string `json:"identity"`
		Peers    []struct {
			Name      string `json:"name"`
			Carrier   string `json:"carrier"`
			Format    string `json:"format"`
			TTY       string `json:"tty_address"`
			Connected bool   `json:"connected"`
			FullName  string `json:"full_name"`
		} `json:"peers"`
	}
	if err := get("/api/status", &out); err != nil {
		return err
	}
	fmt.Printf("%s  %s  (%s)\n\n", out.Identity["designator"], out.Identity["tty"], out.Identity["name"])
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "LINK\tCARRIER\tFORMAT\tADDRESS\tSTATE")
	for _, p := range out.Peers {
		state := "down"
		if p.Connected {
			state = "up"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.FullName, p.Format, p.TTY, state)
	}
	return tw.Flush()
}

// Package view contains the template renderer and view models shared by all
// handlers.
package view

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/atpanos/docktimer/internal/database"
)

// PageData is the top-level data passed to the layout.
type PageData struct {
	Title    string
	Nav      string
	User     *database.User
	CSRF     string
	Flash    string
	Error    string
	TZ       string
	Version  string
	Data     any
	Now      time.Time
	SelfHost string
}

// IsAdmin reports whether the current user may mutate state.
func (p PageData) IsAdmin() bool { return p.User != nil && p.User.Role == database.RoleAdmin }

// Renderer renders full pages and HTMX fragments.
type Renderer struct {
	fsys  fs.FS
	dev   bool
	pages map[string]*template.Template
	frags *template.Template
}

// New parses all templates. In dev mode templates are re-parsed on every render.
func New(fsys fs.FS, dev bool) (*Renderer, error) {
	r := &Renderer{fsys: fsys, dev: dev, pages: map[string]*template.Template{}}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Renderer) load() error {
	entries, err := fs.ReadDir(r.fsys, "pages")
	if err != nil {
		return fmt.Errorf("read template pages: %w", err)
	}
	pages := map[string]*template.Template{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".html")
		t, err := template.New("layout").Funcs(funcMap()).ParseFS(r.fsys,
			"layout.html", "partials.html", "pages/"+e.Name())
		if err != nil {
			return fmt.Errorf("parse page %s: %w", name, err)
		}
		pages[name] = t
	}
	frags, err := template.New("partials").Funcs(funcMap()).ParseFS(r.fsys, "partials.html")
	if err != nil {
		return fmt.Errorf("parse partials: %w", err)
	}
	r.pages = pages
	r.frags = frags
	return nil
}

// Page renders a full page by name.
func (r *Renderer) Page(w io.Writer, page string, data PageData) error {
	if r.dev {
		if err := r.load(); err != nil {
			return err
		}
	}
	t, ok := r.pages[page]
	if !ok {
		return fmt.Errorf("unknown page %q", page)
	}
	return t.ExecuteTemplate(w, "layout", data)
}

// Fragment renders a named partial.
func (r *Renderer) Fragment(w io.Writer, name string, data any) error {
	if r.dev {
		if err := r.load(); err != nil {
			return err
		}
	}
	return r.frags.ExecuteTemplate(w, name, data)
}

// HasPage reports whether a page template exists.
func (r *Renderer) HasPage(name string) bool {
	_, ok := r.pages[name]
	return ok
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"fmtTime": func(t time.Time, tz string) string {
			if t.IsZero() {
				return "—"
			}
			loc, err := time.LoadLocation(tz)
			if err != nil {
				loc = time.UTC
			}
			return t.In(loc).Format("2006-01-02 15:04")
		},
		"shortID": func(id string) string {
			if len(id) > 12 {
				return id[:12]
			}
			return id
		},
		"dur": func(ms int64) string {
			switch {
			case ms < 1000:
				return fmt.Sprintf("%d ms", ms)
			case ms < 60000:
				return fmt.Sprintf("%.1f s", float64(ms)/1000)
			default:
				return fmt.Sprintf("%.0f min", float64(ms)/60000)
			}
		},
		"stateLabel": func(state string) string {
			switch state {
			case "running":
				return "Running"
			case "exited":
				return "Stopped"
			case "created":
				return "Created"
			case "paused":
				return "Paused"
			case "restarting":
				return "Restarting"
			case "dead":
				return "Dead"
			default:
				return state
			}
		},
		"stateClass": func(state string) string {
			switch state {
			case "running":
				return "badge-ok"
			case "restarting", "paused":
				return "badge-warn"
			case "dead":
				return "badge-err"
			default:
				return "badge-muted"
			}
		},
		"actionLabel": func(a database.Action) string {
			switch a {
			case database.ActionStart:
				return "Start"
			case database.ActionStop:
				return "Stop"
			case database.ActionRestart:
				return "Restart"
			default:
				return string(a)
			}
		},
		"statusClass": func(s database.Status) string {
			if s == database.StatusSuccess {
				return "badge-ok"
			}
			return "badge-err"
		},
		"triggerLabel": func(t database.Trigger) string {
			if t == database.TriggerSchedule {
				return "Schedule"
			}
			return "Manual"
		},
		"inTZ": func(t time.Time, tz string) string {
			return t.In(mustLoc(tz)).Format("15:04")
		},
		"navClass": func(current, item string) string {
			base := "rounded-lg px-3 py-2 text-sm font-medium transition-colors "
			if current == item {
				return base + "bg-slate-800 text-white"
			}
			return base + "text-slate-400 hover:bg-slate-800/60 hover:text-slate-200"
		},
	}
}

func mustLoc(tz string) *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// FlashText maps a stable flash code to a user-facing message. Only known codes are
// rendered, so a query parameter can never inject arbitrary text.
func FlashText(code string) string {
	switch code {
	case "logged_out":
		return "You have been signed out."
	case "setup_done":
		return "Admin account created. Welcome to Docktimer!"
	case "rule_created":
		return "Rule created."
	case "rule_updated":
		return "Rule saved."
	case "rule_deleted":
		return "Rule deleted."
	case "rule_toggled":
		return "Rule status changed."
	case "password_changed":
		return "Password changed."
	case "user_created":
		return "User created."
	case "user_deleted":
		return "User deleted."
	case "logs_purged":
		return "Logs purged."
	case "action_ok":
		return "Action completed."
	default:
		return ""
	}
}

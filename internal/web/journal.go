package web

import (
	"context"
	"net/http"
	"net/url"

	"github.com/joestump/msgbrowse/internal/store"
)

// journalPageSize is how many days one /journal render (and each infinite-scroll
// continuation) returns. The journal can span the whole archive's worth of days,
// so it paginates from the first render like the gallery (ADR-0023).
const journalPageSize = 30

// journalData drives the full /journal page.
type journalData struct {
	baseData
	Days    []store.JournalDayView
	NextURL string
}

// journalPageData drives the infinite-scroll continuation fragment. Its Days and
// NextURL fields match journalData's, so the shared journal_page template
// renders identically whether it is invoked inside the full page or on its own.
type journalPageData struct {
	Days    []store.JournalDayView
	NextURL string
}

// handleJournal renders the day-by-day journal: each day newest-first with its
// cached digest, or its mechanical summary when no digest has been generated.
// Boosted navigations swap only #main-content (via the journal_content define);
// a first render or history restore gets the full shell.
func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var base baseData
	if isPartialRequest(r) {
		base = partialBase("Journal · msgbrowse", 0)
	} else {
		var err error
		base, err = s.baseData(ctx, "Journal · msgbrowse", 0)
		if err != nil {
			s.serverError(w, err)
			return
		}
	}
	// Journal is a global surface, neither the Messages nor the Media header tab.
	days, next, err := s.journalPage(ctx, "")
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "journal", journalData{baseData: base, Days: days, NextURL: next})
}

// handleJournalItems serves one keyset continuation page for the journal's
// infinite scroll. after_day is the cursor (the last day already shown); it only
// ever travels as a bound SQL parameter and re-renders through html/template
// escaping. A partial render here never calls baseData (no sidebar work).
func (s *Server) handleJournalItems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days, next, err := s.journalPage(ctx, r.URL.Query().Get("after_day"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, "journal_page", journalPageData{Days: days, NextURL: next})
}

// journalPage fetches one keyset page after afterDay ("" = newest), probing
// journalPageSize+1 rows to detect a further page. It returns the trimmed rows
// and the continuation URL ("" when the walk is done).
func (s *Server) journalPage(ctx context.Context, afterDay string) ([]store.JournalDayView, string, error) {
	days, err := s.store.ListJournalDays(ctx, afterDay, journalPageSize+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(days) > journalPageSize {
		days = days[:journalPageSize]
		next = "/journal/items?after_day=" + url.QueryEscape(days[len(days)-1].Day)
	}
	return days, next, nil
}

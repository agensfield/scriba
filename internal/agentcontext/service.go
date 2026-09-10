package agentcontext

import (
	"context"
	"sort"
	"time"

	"github.com/agensfield/scriba/internal/budget"
	"github.com/agensfield/scriba/internal/budgetadapter"
	"github.com/agensfield/scriba/internal/cache"
	"github.com/agensfield/scriba/internal/model"
	"github.com/agensfield/scriba/internal/resetwatch"
	"github.com/agensfield/scriba/internal/server/store"
)

const defaultEventLimit = 20

type Clock func() time.Time
type Config struct {
	CacheDir, StorePath string
	EventLimit          int
	Clock               Clock
}
type Service struct{ config Config }

func New(config Config) *Service { return &Service{config: config} }

type candidate struct {
	obs         resetwatch.Observation
	lines       []model.MetricLine
	provenance  string
	fromStore   bool
	forcedStale bool
	grantAt     time.Time
	grantStale  bool
}
type readState struct{ cacheErr, storeErr, historyErr, eventErr error }

func (s *Service) Context(ctx context.Context) (Context, error) {
	return s.ContextForAccount(ctx, "")
}

func (s *Service) ContextForAccount(ctx context.Context, requested string) (Context, error) {
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	now := time.Now().UTC()
	if s.config.Clock != nil {
		now = s.config.Clock().UTC()
	}
	limit := s.config.EventLimit
	if limit <= 0 {
		limit = defaultEventLimit
	}
	if limit > 100 {
		limit = 100
	}
	out := Context{SchemaVersion: SchemaVersion, GeneratedAt: now, Sources: []Source{}, Providers: []Provider{}, Events: []Event{}}
	state := readState{}
	cacheCandidates := map[string]candidate{}
	c, err := cache.OpenReadOnlyContext(ctx, s.config.CacheDir)
	if err != nil {
		state.cacheErr = err
	} else {
		snap, loadErr := c.LoadStatusSnapshotContext(ctx)
		_ = c.Close()
		if loadErr != nil {
			state.cacheErr = loadErr
		} else if snap != nil {
			cacheCandidates = candidatesFromSnapshot(*snap)
		}
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	var st *store.Store
	var selectedAccount store.Account
	var storeCandidate candidate
	st, err = store.OpenReadOnlyContext(ctx, s.config.StorePath)
	if err != nil {
		state.storeErr = err
		if requested != "" {
			return Context{}, &AccountError{ReasonCode: "account_unavailable"}
		}
	} else {
		defer func() { _ = st.Close() }()
		var ok bool
		selectedAccount, ok, err = st.ResolveAccount(ctx, requested)
		if err != nil {
			state.storeErr = err
			if requested != "" {
				return Context{}, &AccountError{ReasonCode: "account_unavailable"}
			}
		} else if !ok && requested != "" {
			return Context{}, &AccountError{ReasonCode: "account_unavailable"}
		}
		if err == nil && ok {
			o, observed, loadErr := st.LoadLatestObservationForAccount(ctx, selectedAccount.ID)
			if loadErr != nil {
				state.storeErr = loadErr
			} else if observed && len(budgetadapter.FromResetwatch(o).Windows) > 0 {
				storeCandidate = candidate{obs: o, provenance: "provider-api", fromStore: true, grantAt: o.ObservedAt}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}

	providers := []string{"claude", "codex"}
	for _, providerID := range providers {
		selected, ok := cacheCandidates[providerID]
		accountID, alias := "", ""
		credentialsAvailable := false
		if providerID == "codex" {
			selected, ok = storeCandidate, validCandidate(storeCandidate)
			accountID, alias, credentialsAvailable = selectedAccount.ID, selectedAccount.Alias, selectedAccount.CredentialsAvailable
		}
		if !ok {
			out.Sources = append(out.Sources, missingSources(providerID, state)...)
			if providerID == "codex" && accountID != "" {
				out.Providers = append(out.Providers, buildEmptyProvider(providerID, accountID, alias, credentialsAvailable))
			}
			continue
		}
		history := []budget.Observation(nil)
		hs := budget.HistoryUnavailable
		records := []store.AgentEventRecord(nil)
		if selected.fromStore {
			history, state.historyErr = st.LoadBudgetHistory(ctx, providerID, selected.obs.Account.Ref, selected.obs.ObservedAt.Add(-24*time.Hour))
			if state.historyErr == nil {
				hs = budget.HistoryEmpty
				if len(history) > 0 {
					hs = budget.HistoryAvailable
				}
			}
			records, state.eventErr = st.LoadAgentEvents(ctx, providerID, selected.obs.Account.Ref, limit)
		}
		if err := ctx.Err(); err != nil {
			return Context{}, err
		}
		sources := buildSources(providerID, selected, now, hs, state)
		if selected.fromStore && state.eventErr == nil {
			if len(records) > 0 {
				sources[3] = source(providerID+"-policy-events", "policy-events", "policy-store", now, records[0].DetectedAt, true, false)
			} else {
				sources[3] = Source{SourceID: providerID + "-policy-events", Kind: "policy-events", Availability: "available", Provenance: []Provenance{{Source: "policy-store"}}}
			}
		}
		out.Sources = append(out.Sources, sources...)
		if providerID == "claude" {
			out.Providers = append(out.Providers, buildUnattributedProvider(providerID, selected.obs, history, hs, now))
		} else {
			out.Providers = append(out.Providers, buildProvider(providerID, accountID, alias, credentialsAvailable, selected.obs, history, hs, now))
		}
		if selected.fromStore && state.eventErr == nil {
			for _, r := range records {
				if e, yes := minimize(r, accountID); yes {
					out.Events = append(out.Events, e)
				}
			}
		}
	}
	sort.Slice(out.Providers, func(i, j int) bool { return out.Providers[i].ProviderID < out.Providers[j].ProviderID })
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].SourceID < out.Sources[j].SourceID })
	sort.SliceStable(out.Events, func(i, j int) bool {
		if out.Events[i].DetectedAt.Equal(out.Events[j].DetectedAt) {
			return out.Events[i].ID > out.Events[j].ID
		}
		return out.Events[i].DetectedAt.After(out.Events[j].DetectedAt)
	})
	return out, nil
}

func candidatesFromSnapshot(s model.StatusSnapshot) map[string]candidate {
	out := map[string]candidate{}
	at, err := time.Parse(time.RFC3339Nano, s.GeneratedAt)
	if err != nil {
		return out
	}
	for _, p := range s.Providers {
		if p.ProviderID != "codex" && p.ProviderID != "claude" {
			continue
		}
		observedAt, forcedStale := cacheObservationState(p, at, false)
		grantAt, grantStale := cacheObservationState(p, observedAt, true)
		o := budgetadapter.FromMetricLines(p.ProviderID, observedAt, p.Lines)
		if len(o.Windows) == 0 {
			continue
		}
		obs := resetwatch.Observation{ProviderID: p.ProviderID, ObservedAt: observedAt, Windows: toWindows(p.ProviderID, observedAt, p.Lines), ResetGrants: resetwatch.ResetGrantsFromMetricLines(p.Lines)}
		out[p.ProviderID] = candidate{obs: obs, lines: p.Lines, provenance: "status-cache", forcedStale: forcedStale, grantAt: grantAt, grantStale: grantStale}
	}
	return out
}

func cacheObservationState(provider model.ProviderSnapshot, fallback time.Time, grants bool) (time.Time, bool) {
	observedAt := fallback
	haveProviderTime := false
	forcedStale := false
	provenance := append([]model.SourceProvenance{}, provider.Provenance...)
	for _, line := range provider.Lines {
		if grants {
			if line.Label != resetwatch.LabelResetGrants && line.Label != resetwatch.LabelGrantExpiry {
				continue
			}
		} else {
			if line.Type != "progress" {
				continue
			}
			if _, ok := budgetadapter.WindowKey(provider.ProviderID, line.Label); !ok {
				continue
			}
		}
		provenance = append(provenance, line.Provenance...)
	}
	for _, item := range provenance {
		if item.ProviderID != "" && item.ProviderID != provider.ProviderID {
			continue
		}
		if item.Kind == "cache" && item.Stale {
			forcedStale = true
			continue
		}
		if item.Kind != "provider-api" {
			continue
		}
		if item.Error != "" {
			forcedStale = true
			continue
		}
		if item.Stale {
			forcedStale = true
		}
		if fetchedAt, err := time.Parse(time.RFC3339Nano, item.FetchedAt); err == nil && (!haveProviderTime || fetchedAt.After(observedAt)) {
			observedAt, haveProviderTime = fetchedAt, true
		}
	}
	return observedAt, forcedStale
}
func validCandidate(c candidate) bool {
	return c.obs.ProviderID != "" && len(budgetadapter.FromResetwatch(c.obs).Windows) > 0
}
func toWindows(provider string, at time.Time, lines []model.MetricLine) []resetwatch.Window {
	o := budgetadapter.FromMetricLines(provider, at, lines)
	out := make([]resetwatch.Window, 0, len(o.Windows))
	for _, w := range o.Windows {
		ms := w.PeriodDuration.Milliseconds()
		out = append(out, resetwatch.Window{Label: w.Label, UsedPercent: w.UsedPercent, ResetAt: w.ResetAt, PeriodDurationMs: &ms})
	}
	return out
}

func missingSources(provider string, state readState) []Source {
	provenance := "status-cache"
	reason := "missing"
	readFailed := state.cacheErr != nil
	if provider == "codex" {
		provenance = "provider-api"
		readFailed = state.storeErr != nil
	}
	if readFailed {
		reason = "read_error"
	}
	eventReason := "missing"
	if provider == "codex" && state.storeErr != nil {
		eventReason = "read_error"
	}
	return []Source{{SourceID: provider + "-quota", Kind: "quota", Availability: "unavailable", Provenance: []Provenance{{Source: provenance}}, ReasonCode: reason}, {SourceID: provider + "-budget", Kind: "budget", Availability: "unavailable", Provenance: []Provenance{{Source: "budget-current"}}, ReasonCode: reason}, {SourceID: provider + "-grants", Kind: "grants", Availability: "unavailable", Provenance: []Provenance{{Source: provenance}}, ReasonCode: reason}, {SourceID: provider + "-policy-events", Kind: "policy-events", Availability: "unavailable", Provenance: []Provenance{{Source: "policy-store"}}, ReasonCode: eventReason}}
}
func buildSources(provider string, c candidate, now time.Time, hs budget.HistoryState, state readState) []Source {
	q := source(provider+"-quota", "quota", c.provenance, now, c.obs.ObservedAt, true, false)
	grantsKnown := c.obs.ResetGrants.AvailableCount != nil || len(c.obs.ResetGrants.Credits) > 0 || !c.obs.ResetGrants.ExpiresAt.IsZero() || hasGrantLine(c.lines)
	grantAt := c.grantAt
	if grantAt.IsZero() {
		grantAt = c.obs.ObservedAt
	}
	g := source(provider+"-grants", "grants", c.provenance, now, grantAt, grantsKnown, false)
	b := source(provider+"-budget", "budget", "budget-current", now, c.obs.ObservedAt, true, false)
	if c.forcedStale {
		q = forceStale(q)
		b = forceStale(b)
	}
	if c.grantStale && grantsKnown {
		g = forceStale(g)
	}
	b.ObservedAt = nil
	gen := now
	b.GeneratedAt = &gen
	if c.fromStore && state.historyErr == nil {
		b.Provenance = append(b.Provenance, Provenance{Source: "budget-history"})
	}
	if state.historyErr != nil {
		b.Availability = "unavailable"
		b.ReasonCode = "read_error"
	} else if hs == budget.HistoryUnavailable {
		b.Availability = "degraded"
		b.ReasonCode = "history_unavailable"
	}
	e := Source{SourceID: provider + "-policy-events", Kind: "policy-events", Availability: "unavailable", Provenance: []Provenance{{Source: "policy-store"}}, ReasonCode: "missing"}
	if c.fromStore && state.eventErr != nil {
		e.ReasonCode = "read_error"
	}
	return []Source{q, b, g, e}
}

func forceStale(source Source) Source {
	stale := true
	source.Stale = &stale
	source.Availability = "degraded"
	source.ReasonCode = "stale"
	return source
}

func hasGrantLine(lines []model.MetricLine) bool {
	for _, line := range lines {
		if line.Label == resetwatch.LabelResetGrants || line.Label == resetwatch.LabelGrantExpiry {
			return true
		}
	}
	return false
}
func source(id, kind, provenance string, now, at time.Time, available, readErr bool) Source {
	s := Source{SourceID: id, Kind: kind, Availability: "unavailable", Provenance: []Provenance{{Source: provenance}}, ReasonCode: "missing"}
	if readErr {
		s.ReasonCode = "read_error"
	}
	if !available {
		return s
	}
	age := now.Sub(at).Milliseconds()
	if age < 0 {
		age = 0
	}
	stale := age > 15*60*1000
	s.ObservedAt = &at
	s.AgeMS = &age
	s.Stale = &stale
	s.Availability = "available"
	s.ReasonCode = ""
	if stale {
		s.Availability = "degraded"
		s.ReasonCode = "stale"
	}
	return s
}
func buildProvider(id, accountID, alias string, credentialsAvailable bool, obs resetwatch.Observation, h []budget.Observation, hs budget.HistoryState, now time.Time) Provider {
	r := budget.Evaluate(budget.Input{ProviderID: id, Observation: budgetadapter.FromResetwatch(obs), History: h, HistoryState: hs}, now)
	a := Account{AccountID: accountID, Alias: alias, CredentialsAvailable: credentialsAvailable, Windows: []Window{}, Budgets: []Budget{}, Grants: aggregateGrants(obs.ResetGrants), SourceIDs: []string{id + "-quota", id + "-budget", id + "-grants", id + "-policy-events"}}
	for _, w := range r.Windows {
		a.Windows = append(a.Windows, Window{Key: string(w.Key), UsedPercent: w.UsedPercent, RemainingPercentPoints: w.RemainingPercentPoints, ResetAt: w.ResetAt})
		a.Budgets = append(a.Budgets, Budget{Key: string(w.Key), Risk: w.Risk, Confidence: w.Confidence, Reasons: w.Reasons})
	}
	return Provider{ProviderID: id, Accounts: []Account{a}}
}

func buildEmptyProvider(id, accountID, alias string, credentialsAvailable bool) Provider {
	return Provider{ProviderID: id, Accounts: []Account{{
		AccountID: accountID, Alias: alias, CredentialsAvailable: credentialsAvailable,
		Windows: []Window{}, Budgets: []Budget{}, Grants: Grants{},
		SourceIDs: []string{id + "-quota", id + "-budget", id + "-grants", id + "-policy-events"},
	}}}
}

func buildUnattributedProvider(id string, obs resetwatch.Observation, h []budget.Observation, hs budget.HistoryState, now time.Time) Provider {
	evaluated := budget.Evaluate(budget.Input{ProviderID: id, Observation: budgetadapter.FromResetwatch(obs), History: h, HistoryState: hs}, now)
	usage := &Unattributed{Windows: []Window{}, Budgets: []Budget{}, Grants: aggregateGrants(obs.ResetGrants), SourceIDs: []string{id + "-quota", id + "-budget", id + "-grants", id + "-policy-events"}}
	for _, w := range evaluated.Windows {
		usage.Windows = append(usage.Windows, Window{Key: string(w.Key), UsedPercent: w.UsedPercent, RemainingPercentPoints: w.RemainingPercentPoints, ResetAt: w.ResetAt})
		usage.Budgets = append(usage.Budgets, Budget{Key: string(w.Key), Risk: w.Risk, Confidence: w.Confidence, Reasons: w.Reasons})
	}
	return Provider{ProviderID: id, Accounts: []Account{}, Unattributed: usage}
}
func aggregateGrants(g resetwatch.ResetGrants) Grants {
	count := 0
	if g.AvailableCount != nil {
		count = *g.AvailableCount
	}
	var expiry *time.Time
	for _, c := range g.Credits {
		if c.Status != "available" || c.ExpiresAt.IsZero() {
			continue
		}
		if expiry == nil || c.ExpiresAt.Before(*expiry) {
			v := c.ExpiresAt
			expiry = &v
		}
	}
	if expiry == nil && !g.ExpiresAt.IsZero() {
		v := g.ExpiresAt
		expiry = &v
	}
	return Grants{AvailableCount: count, EarliestExpiryAt: expiry}
}
func minimize(r store.AgentEventRecord, accountID string) (Event, bool) {
	e := Event{SchemaVersion: "scriba.event.v2", ID: r.ID, ProviderID: r.ProviderID, AccountID: accountID, Kind: string(r.Kind), DetectedAt: r.DetectedAt}
	switch e.Kind {
	case "remaining_checkpoint":
		if r.UsedPercent == nil || r.RemainingPercentPoints == nil {
			return Event{}, false
		}
		e.Data = RemainingCheckpoint{WindowKey: string(r.WindowKey), CheckpointPercent: r.Checkpoint, UsedPercent: *r.UsedPercent, RemainingPercentPoints: *r.RemainingPercentPoints, ResetAt: r.ResetAt}
	case "reset_transition":
		if r.PreviousResetAt == nil || r.ResetAt == nil {
			return Event{}, false
		}
		e.Data = ResetTransition{WindowKey: string(r.WindowKey), ResetKind: r.ResetKind, PreviousResetAt: *r.PreviousResetAt, ResetAt: *r.ResetAt}
	case "grant_available":
		if r.AvailableCount == nil {
			return Event{}, false
		}
		e.Data = GrantAvailable{AvailableCount: *r.AvailableCount, EarliestExpiryAt: r.ExpiresAt}
	case "grant_expiry_checkpoint":
		if r.ExpiresAt == nil {
			return Event{}, false
		}
		e.Data = GrantExpiryCheckpoint{CheckpointDays: r.Checkpoint, ExpiresAt: *r.ExpiresAt}
	default:
		return Event{}, false
	}
	return e, true
}

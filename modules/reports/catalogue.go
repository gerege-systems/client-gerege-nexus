/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The reporting engine, answering the questions the app's rules ask of it.
 */

package reports

import (
	domain "github.com/gerege-systems/client-gerege-nexus/domain/reports"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// catalogue is domain/reports.Catalogue over nexus.ReportEngine.
//
// It is a translation and nothing else: a registered report becomes the four
// facts the rules need — its key, its app, its names and the sharing scopes it
// was written for — and the validators forward the engine's own refusal.
//
// Over the contract rather than over internal/platform/reporting, which is what
// this app called until 2026-08-23. Everything it used is one of six methods
// now, and the two it had no method for — a schedule's validity and a
// consolidated run — are the two the contract grew.
type catalogue struct{ engine nexus.ReportEngine }

func (c catalogue) Report(key string) (domain.Report, bool) {
	described, found := c.engine.Describe(key)
	if !found {
		return domain.Report{}, false
	}
	return describe(described), true
}

func (c catalogue) ForApps(installed map[string]bool) []domain.Report {
	permitted := c.engine.Available(installed)
	described := make([]domain.Report, 0, len(permitted))
	for _, report := range permitted {
		described = append(described, describe(report))
	}
	return described
}

// Title resolves a report's name in the caller's language.
//
// Done here rather than asked of the engine: it is a map lookup with two
// fallbacks — the language asked for, then English, then the key itself — and a
// contract method for it would be a network of one call to pick a map entry.
func (catalogue) Title(report domain.Report, locale string) string {
	if title := report.Titles[locale]; title != "" {
		return title
	}
	if title := report.Titles["en"]; title != "" {
		return title
	}
	return report.Key
}

// ValidateParams, ValidateCron and NormalizeFormat are one call to the engine.
//
// They were three package functions — Bind, ParseCron, ParseFormat — and the
// contract offers them as ValidateSchedule, deliberately: a schedule accepted
// with three of the four checked is a schedule that fails at three in the
// morning to nobody. The domain still asks its three questions and each is
// answered by the whole check, which refuses more rather than less.
func (c catalogue) ValidateParams(key string, params map[string]string, locale string) error {
	if _, found := c.engine.Describe(key); !found {
		return nil
	}
	// A whole schedule is what the contract validates, so the two fields this
	// check is not about are given values it will accept: every minute, and the
	// format every deployment can render. What is under question is the
	// parameters, and they are the ones the caller supplied.
	return c.engine.ValidateSchedule(key, params, locale, "* * * * *", "xlsx")
}

func (c catalogue) ValidateCron(expression string) error {
	return c.engine.ValidateCron(expression)
}

func (c catalogue) NormalizeFormat(raw string) (string, error) {
	return c.engine.NormalizeFormat(raw)
}

// describe is the one place a catalogue entry becomes the domain's value.
func describe(report nexus.ReportDescription) domain.Report {
	return domain.Report{
		Key:    report.Key,
		App:    report.App,
		Titles: report.Titles,
		Scopes: report.Scopes,
	}
}

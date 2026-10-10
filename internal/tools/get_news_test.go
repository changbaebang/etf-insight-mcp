package tools

import "testing"

func TestGetNews(t *testing.T) {
	sess, _, fund := newDataSession(t)

	t.Run("default limit", func(t *testing.T) {
		var out getNewsOutput
		callOK(t, sess, "get_news", map[string]any{"query": " SCHD "}, &out)
		if out.Query != "SCHD" || out.Count != defaultNewsLimit || len(out.News) != defaultNewsLimit {
			t.Fatalf("query/count = %q/%d, want SCHD/%d", out.Query, out.Count, defaultNewsLimit)
		}
		if _, news := fund.limits(); news != defaultNewsLimit {
			t.Errorf("upstream limit = %d, want %d", news, defaultNewsLimit)
		}
		want := newsRow{Title: "Headline 1 about SCHD", Publisher: "Fake Wire", Link: "https://example.com/news/1", PublishedAt: "2024-01-02T21:00:00Z"}
		if out.News[0] != want {
			t.Errorf("first = %+v, want %+v", out.News[0], want)
		}
		if out.News[1].PublishedAt != "" {
			t.Errorf("undated headline published_at = %q, want empty", out.News[1].PublishedAt)
		}
	})

	t.Run("maximum limit", func(t *testing.T) {
		var out getNewsOutput
		callOK(t, sess, "get_news", map[string]any{"query": "treasury yields", "limit": 20}, &out)
		if out.Count != maxNewsLimit {
			t.Errorf("count = %d, want %d", out.Count, maxNewsLimit)
		}
	})

	t.Run("no headlines", func(t *testing.T) {
		var out getNewsOutput
		callOK(t, sess, "get_news", map[string]any{"query": "nothing"}, &out)
		if out.Count != 0 || out.News == nil || !hasDataNote(out.Notes, "no headlines") {
			t.Errorf("out = %+v", out)
		}
	})

	errs := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "limit too high", args: map[string]any{"query": "SCHD", "limit": 21}, want: "limit must be between 1 and 20, got 21"},
		{name: "empty query", args: map[string]any{"query": ""}, want: "query is required"},
		{name: "missing query", args: map[string]any{}, want: "query"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			callErr(t, sess, "get_news", tt.args, tt.want)
		})
	}
}

func TestGetNewsNonLatinQuery(t *testing.T) {
	sess, _, _ := newDataSession(t)
	callErr(t, sess, "get_news", map[string]any{"query": "미국 배당"}, "Latin")
	out := callRaw(t, sess, "get_news", map[string]any{"query": "미국 ETF"})
	if !hasDataNote(rawStrings(out["notes"]), "ignores") {
		t.Errorf("notes = %v, want a caution that the non-Latin words are ignored", out["notes"])
	}
}

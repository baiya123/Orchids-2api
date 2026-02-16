package grok

import "testing"

func TestNormalizeGeneratedImageURLsPrefersGrokAsset(t *testing.T) {
	in := []string{
		"https://example.com/traffic.jpg",
		"https://assets.grok.com/users/u/generated/a/image.jpg",
	}
	got := normalizeGeneratedImageURLs(in, 1)
	if len(got) != 1 {
		t.Fatalf("len=%d want=1", len(got))
	}
	if got[0] != "https://assets.grok.com/users/u/generated/a/image.jpg" {
		t.Fatalf("got=%q want grok asset url", got[0])
	}
}

func TestNormalizeGeneratedImageURLsPrefersFullOverPart(t *testing.T) {
	in := []string{
		"https://assets.grok.com/users/u/generated/a-part-0/image.jpg",
		"https://assets.grok.com/users/u/generated/a/image.jpg",
	}
	got := normalizeGeneratedImageURLs(in, 0)
	if len(got) != 1 {
		t.Fatalf("len=%d want=1", len(got))
	}
	if got[0] != "https://assets.grok.com/users/u/generated/a/image.jpg" {
		t.Fatalf("got=%q want full url", got[0])
	}
}

func TestAppendImageCandidatesPrefersGrokPath(t *testing.T) {
	debugHTTP := []string{"https://example.com/other.jpg"}
	debugAsset := []string{"users/u/generated/a/image.jpg"}
	got := appendImageCandidates(nil, debugHTTP, debugAsset, 1)
	if len(got) != 1 {
		t.Fatalf("len=%d want=1", len(got))
	}
	if got[0] != "https://assets.grok.com/users/u/generated/a/image.jpg" {
		t.Fatalf("got=%q want grok asset url", got[0])
	}
}

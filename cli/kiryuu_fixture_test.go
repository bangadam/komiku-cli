package cli

import (
	"fmt"
	"strings"
)

// kiryuuCard renders one series card in the markup the Kiryuu catalogue
// parsers expect: a card container with a cover link, a title link, and a
// status badge.
func kiryuuCard(href, title, status string) string {
	return fmt.Sprintf(`<div class="flex rounded-lg overflow-hidden h-46">
<a href="%s" class="min-w-[120px] w-23 h-full relative"><img src="https://cdn.kiryuu.io/kuma/%s/cover.jpg" alt="%s"></a>
<a href="%s" class="text-base font-medium">%s</a>
<div><p class="inline-block">Chapter 1</p><span class="bg-accent">%s</span><time>2 hours ago</time></div>
</div>`, href, strings.Trim(title, " "), title, href, title, status)
}

// kiryuuChapterFixture describes one chapter link on a fixture series page.
type kiryuuChapterFixture struct {
	ID     string
	Number string
}

// kiryuuSeriesPage renders a series page with one chapter link per fixture, in
func kiryuuSeriesPage(origin, series string, chapters []kiryuuChapterFixture) string {
	origin = strings.TrimSuffix(origin, "/")
	var page strings.Builder
	fmt.Fprintf(&page, `<h1>%s</h1>`, series)
	for _, chapter := range chapters {
		fmt.Fprintf(&page, `<div><a href="%s/manga/%s/chapter-%s.%s/">Chapter %s</a></div>`,
			origin, series, chapter.Number, chapter.ID, chapter.Number)
	}
	return page.String()
}

// kiryuuChapterObjectJSON is one /wp-json/kiru/v1/chapter object. The chapter
// URL uses the canonical /manga/<series>/chapter-<number>.<postID>/ form.
func kiryuuChapterObjectJSON(series, id, number, origin, content string) string {
	slug := "chapter-" + number + "." + id
	return fmt.Sprintf(`{"id":%s,"slug":%q,"title":%q,"number":%q,"url":%q,"content":%q,"parent_id":42}`,
		id, slug, "Fixture Chapter "+number, number, strings.TrimSuffix(origin, "/")+"/manga/"+series+"/"+slug+"/", content)
}

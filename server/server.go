// Package server serves a local Komiku download library as a private,
// offline manga reader over HTTP. It reads chapter directories and CBZ
// archives directly; it never contacts Komiku.
package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bangadam/komiku-cli/store"
)

// Library entry: one series directory under the configured root.
type Series struct {
	Slug     string   `json:"slug"`
	Title    string   `json:"title"`
	Volumes  []Volume `json:"volumes"`
	Chapters []string `json:"chapters"`
	CBZ      []string `json:"cbz"`
}

type Volume struct {
	Number   int      `json:"number"`
	Chapters []string `json:"chapters"`
}

// Server is the offline library + reader HTTP server.
type Server struct {
	root   string
	mux    *http.ServeMux
}

// New constructs a server rooted at the given download root.
func New(root string) (*Server, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("library root is empty")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat library root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("library root %q is not a directory", root)
	}
	s := &Server{root: root, mux: http.NewServeMux()}
	s.mux.HandleFunc("/api/library", s.handleLibrary)
	s.mux.HandleFunc("/api/series/", s.handleSeries)
	s.mux.HandleFunc("/api/pages/", s.handlePages)
	s.mux.HandleFunc("/api/page/", s.handlePage)
	s.mux.HandleFunc("/", s.handleUI)
	return s, nil
}

// handleUI serves the single-page reader. Everything (HTML, CSS, JS) is
// inlined so the server has zero external dependencies and works on a
// phone or tablet over LAN with no internet.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(readerHTML))
}

func (s *Server) Handler() http.Handler { return s.mux }

// Serve serves the library over the given listener, shutting down on ctx
// cancellation.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpServer := &http.Server{Handler: s.mux}
	go func() {
		<-ctx.Done()
		_ = httpServer.Shutdown(context.Background())
	}()
	if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ListenAndServe starts the HTTP listener.
func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.mux)
}

// Root returns the configured library root.
func (s *Server) Root() string { return s.root }

// scanSeries lists every series directory under the root. A series is any
// directory containing at least one chapter-* folder or a .cbz archive.
func (s *Server) scanSeries() []Series {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil
	}
	var series []Series
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		dir := filepath.Join(s.root, slug)
		info, err := scanSeriesDir(dir)
		if err != nil || (len(info.Chapters) == 0 && len(info.CBZ) == 0 && len(info.Volumes) == 0) {
			continue
		}
		info.Slug = slug
		info.Title = prettify(slug)
		series = append(series, info)
	}
	sort.Slice(series, func(i, j int) bool { return series[i].Slug < series[j].Slug })
	return series
}

func scanSeriesDir(dir string) (Series, error) {
	info := Series{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return info, err
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir() && isVolumeDir(name):
			vol := Volume{Number: parseVolumeNumber(name)}
			volDir := filepath.Join(dir, name)
			volEntries, err := os.ReadDir(volDir)
			if err != nil {
				continue
			}
			for _, ve := range volEntries {
				if ve.IsDir() && strings.HasPrefix(ve.Name(), "chapter-") {
					vol.Chapters = append(vol.Chapters, ve.Name())
				}
			}
			sort.Strings(vol.Chapters)
			info.Volumes = append(info.Volumes, vol)
		case entry.IsDir() && strings.HasPrefix(name, "chapter-"):
			info.Chapters = append(info.Chapters, name)
		case strings.HasSuffix(strings.ToLower(name), ".cbz"):
			info.CBZ = append(info.CBZ, name)
		}
	}
	sort.Strings(info.Chapters)
	sort.Strings(info.CBZ)
	sort.Slice(info.Volumes, func(i, j int) bool { return info.Volumes[i].Number < info.Volumes[j].Number })
	return info, nil
}

// handleLibrary returns the full library index as JSON.
func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"series": s.scanSeries()})
}

// handleSeries returns one series detail: chapters with their page counts
// derived offline (directory listings and CBZ central directories).
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/api/series/")
	slug = strings.TrimSuffix(slug, "/")
	if slug == "" || strings.ContainsAny(slug, "/\\\x00") {
		http.NotFound(w, r)
		return
	}
	dir := filepath.Join(s.root, slug)
	if info, err := scanSeriesDir(dir); err == nil {
		info.Slug = slug
		info.Title = prettify(slug)
		// Enrich chapters with page counts.
		type chapterDetail struct {
			Dir      string `json:"dir"`
			Pages    int    `json:"pages"`
			Source   string `json:"source"`
			Volume   int    `json:"volume,omitempty"`
			Done     bool   `json:"done"`
		}
		var chapters []chapterDetail
		for _, name := range info.Chapters {
			pages, _ := countDirPages(filepath.Join(dir, name))
			chapters = append(chapters, chapterDetail{Dir: name, Pages: pages, Source: "folder", Done: isChapterDone(dir, name)})
		}
		for _, vol := range info.Volumes {
			for _, name := range vol.Chapters {
				pages, _ := countDirPages(filepath.Join(dir, fmt.Sprintf("vol-%02d", vol.Number), name))
				chapters = append(chapters, chapterDetail{Dir: name, Pages: pages, Source: "folder", Volume: vol.Number, Done: isChapterDone(dir, name)})
			}
		}
		for _, cbz := range info.CBZ {
			pages, _ := countCBZPages(filepath.Join(dir, cbz))
			chapters = append(chapters, chapterDetail{Dir: cbz, Pages: pages, Source: "cbz", Done: true})
		}
		writeJSON(w, map[string]any{
			"slug":     info.Slug,
			"title":    info.Title,
			"chapters": chapters,
			"cbz":      info.CBZ,
			"volumes":  info.Volumes,
		})
		return
	}
	http.NotFound(w, r)
}

// handlePages returns the page list (filenames) for one chapter folder or
// CBZ archive. Path: /api/pages/<slug>/<chapter-dir-or-cbz>
func (s *Server) handlePages(w http.ResponseWriter, r *http.Request) {
	target, ok := s.resolveChapter(r, "/api/pages/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	pages, err := listPages(target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"pages": pages})
}

// handlePage streams a single image from a chapter folder or a CBZ entry.
// Path: /api/page/<slug>/<chapter-dir-or-cbz>?p=001.jpg
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	target, ok := s.resolveChapter(r, "/api/page/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	page := r.URL.Query().Get("p")
	if page == "" || strings.ContainsAny(page, "\x00") || isTraversal(page) {
		http.Error(w, "missing or invalid page", http.StatusBadRequest)
		return
	}
	if isCBZPath(target) {
		if err := serveCBZPage(target, page, w); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}
	http.ServeFile(w, r, filepath.Join(target, page))
}

// resolveChapter maps /api/<prefix>/<slug>/<chapter> to a real filesystem path.
func (s *Server) resolveChapter(r *http.Request, prefix string) (string, bool) {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	slug, chapter := parts[0], parts[1]
	if isTraversal(slug) || isTraversal(chapter) || strings.ContainsAny(chapter, "\x00") {
		return "", false
	}
	// Mapped layout: try vol-XX/chapter first; flat otherwise.
	mappedGuess := filepath.Join(s.root, slug, chapter)
	if _, err := os.Stat(mappedGuess); err == nil {
		return mappedGuess, true
	}
	// Search volume folders for the chapter dir.
	volEntries, err := os.ReadDir(filepath.Join(s.root, slug))
	if err != nil {
		return "", false
	}
	for _, entry := range volEntries {
		if !entry.IsDir() || !isVolumeDir(entry.Name()) {
			continue
		}
		candidate := filepath.Join(s.root, slug, entry.Name(), chapter)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func listPages(target string) ([]string, error) {
	if isCBZPath(target) {
		return listCBZEntries(target)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	var pages []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if isImageName(entry.Name()) {
			pages = append(pages, entry.Name())
		}
	}
	sort.Strings(pages)
	return pages, nil
}

func countDirPages(dir string) (int, error) {
	pages, err := listPages(dir)
	if err != nil {
		return 0, err
	}
	return len(pages), nil
}

func countCBZPages(path string) (int, error) {
	entries, err := listCBZEntries(path)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func listCBZEntries(path string) ([]string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var pages []string
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(file.Name)
		if isImageName(base) {
			pages = append(pages, file.Name)
		}
	}
	sort.Strings(pages)
	return pages, nil
}

// isTraversal rejects ".." path components that escape the library root.
// Plain "." is allowed because CBZ filenames legitimately contain dots.
func isTraversal(part string) bool {
	for _, segment := range strings.Split(part, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}


func serveCBZPage(path, page string, w http.ResponseWriter) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.Name != page && filepath.Base(file.Name) != page {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		w.Header().Set("Content-Type", contentTypeFor(page))
		_, err = io.Copy(w, rc)
		return err
	}
	return fmt.Errorf("page %q not found in archive", page)
}

func isChapterDone(seriesDir, chapterName string) bool {
	// Presence of pages implies done for the reader; a rigorous check is
	// available via `komiku-cli verify`.
	pages, err := countDirPages(filepath.Join(seriesDir, chapterName))
	if err == nil && pages > 0 {
		return true
	}
	return false
}

func isImageName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	}
	return false
}

func isCBZPath(path string) bool { return strings.EqualFold(filepath.Ext(path), ".cbz") }

func contentTypeFor(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return mime.TypeByExtension(ext)
	}
}

func isVolumeDir(name string) bool {
	if !strings.HasPrefix(name, "vol-") {
		return false
	}
	rest := strings.TrimPrefix(name, "vol-")
	_, err := strconv.Atoi(rest)
	return err == nil
}

const readerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no">
<title>komiku-cli library</title>
<style>
:root{--bg:#0e0e0e;--panel:#1a1a1a;--text:#e6e6e6;--muted:#888;--accent:#6cb4ff}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,Segoe UI,sans-serif}
header{padding:14px 18px;border-bottom:1px solid #2a2a2a;display:flex;gap:12px;align-items:center}
h1{font-size:17px;margin:0;font-weight:600}
.muted{color:var(--muted)}
main{display:flex;height:calc(100vh - 57px)}
nav{width:280px;border-right:1px solid #2a2a2a;overflow-y:auto;padding:10px}
nav .series{padding:9px 10px;border-radius:8px;cursor:pointer;margin-bottom:4px}
nav .series:hover{background:var(--panel)}
nav .series.active{background:#243b57;color:#fff}
nav .series .count{color:var(--muted);font-size:12px}
aside{flex:1;overflow-y:auto;padding:18px}
.chapters{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:10px}
.chapter{background:var(--panel);border-radius:10px;padding:14px;cursor:pointer}
.chapter:hover{background:#222}
.chapter .meta{color:var(--muted);font-size:12px;margin-top:4px}
.chapter.done::after{content:"OK";float:right;color:#4ade80;font-weight:700}
#reader{position:fixed;inset:0;background:#000;z-index:50;display:none;flex-direction:column}
#reader.show{display:flex}
#reader .bar{display:flex;align-items:center;gap:14px;padding:10px 14px;background:#111;border-bottom:1px solid #222}
#reader .bar button{background:#333;color:#fff;border:0;border-radius:6px;padding:6px 12px;cursor:pointer;font-size:14px}
#reader .bar button:hover{background:#444}
#reader .bar .pos{color:var(--muted);min-width:90px}
#reader .stage{flex:1;overflow-y:auto;display:flex;flex-direction:column;align-items:center}
#reader .stage img{max-width:100%;margin:auto}
@media(max-width:640px){nav{width:160px}main{flex-direction:column}nav{height:200px}}
</style>
</head>
<body>
<header><h1>komiku-cli library</h1><span class="muted" id="root"></span></header>
<main>
<nav id="nav"></nav>
<aside><div id="content" class="muted">Select a series.</div></aside>
</main>
<div id="reader">
<div class="bar">
<button id="close">close</button>
<button id="prev">&laquo; prev</button>
<span class="pos" id="pos"></span>
<button id="next">next &raquo;</button>
</div>
<div class="stage" id="stage"><img id="img"></div>
</div>
</body>
<script>
let library=[], current=null, chapter=null, pages=[], pageIdx=0;
async function init(){const r=await fetch('/api/library');const d=await r.json();library=d.series||[];renderNav();}
function renderNav(){const n=document.getElementById('nav');n.innerHTML='';
if(!library.length){document.getElementById('content').textContent='No series found.';return;}
library.forEach(s=>{const d=document.createElement('div');d.className='series';d.onclick=()=>selectSeries(s.slug,d);
d.innerHTML='<div>'+s.title+'</div><div class="count">'+(s.chapters.length+s.cbz.length)+' chapters</div>';n.appendChild(d);});}
async function selectSeries(slug,el){document.querySelectorAll('.series').forEach(x=>x.classList.remove('active'));el.classList.add('active');
const r=await fetch('/api/series/'+slug);const d=await r.json();current=slug;renderChapters(d);}
function renderChapters(d){const c=document.getElementById('content');c.innerHTML='';
if(!d.chapters||!d.chapters.length){c.textContent='No chapters.';return;}
const wrap=document.createElement('div');wrap.className='chapters';
d.chapters.forEach(ch=>{const e=document.createElement('div');e.className='chapter'+(ch.done?' done':'');
e.onclick=()=>openChapter(current,ch.dir,ch.volume);
e.innerHTML='<div>'+ch.dir+'</div><div class="meta">'+ch.pages+' pages &middot; '+ch.source+'</div>';wrap.appendChild(e);});
c.appendChild(wrap);}
async function openChapter(slug,dir,vol){chapterDir=dir;window.curVol=vol;let path=slug+'/'+dir;if(vol)path=slug+'/vol-'+String(vol).padStart(2,'0')+'/'+dir;
const r=await fetch('/api/pages/'+path);const d=await r.json();pages=d.pages||[];pageIdx=0;document.getElementById('reader').classList.add('show');show();}
function show(){if(!pages.length){document.getElementById('pos').textContent='empty';return;}
const p=pages[pageIdx];let path=current+'/'+chapterDir;if(window.curVol)path=current+'/vol-'+String(window.curVol).padStart(2,'0')+'/'+chapterDir;
document.getElementById('img').src='/api/page/'+path+'?p='+encodeURIComponent(p);
document.getElementById('pos').textContent=(pageIdx+1)+' / '+pages.length;}
let chapterDir='';
</html>`

func parseVolumeNumber(name string) int {
	rest := strings.TrimPrefix(name, "vol-")
	n, _ := strconv.Atoi(rest)
	return n
}

func prettify(slug string) string {
	words := strings.FieldsFunc(strings.ReplaceAll(slug, "-", " "), func(r rune) bool {
		return r == ' ' || r == '_' || r == '.'
	})
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// ReadDoneState is exported so callers (tests) can inspect state. It reuses
// the store package's read-only helper.
func ReadDoneState(root, series string) ([]float64, error) {
	return store.ReadDone(root, series)
}

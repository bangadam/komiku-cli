package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bangadam/komiku-cli/komiku"
	"github.com/bangadam/komiku-cli/store"
)

type Status string

const (
	Done  Status = "DONE"
	Part  Status = "PART"
	Fail  Status = "FAIL"
	NoImg Status = "NOIMG"
)

type Job struct {
	Chapter   komiku.Chapter
	Volume    int
	Flat      bool
	Ambiguous bool
}

type Result struct {
	Chapter   komiku.Chapter
	Status    Status
	Success   int
	Total     int
	Errors    []string
	SourceDir string
}

func (r Result) Label() string {
	if r.Status == Part {
		return fmt.Sprintf("PART (%d/%d)", r.Success, r.Total)
	}
	return string(r.Status)
}

type EventKind uint8

const (
	BatchStarted EventKind = iota
	ChapterStarted
	ChapterPagesKnown
	PageStarted
	PageSkipped
	PageDone
	PageFailed
	ChapterFinished
	BatchPaused
	BatchResumed
	BatchStopping
	BatchFinished
)

type Event struct {
	Sequence uint64
	At       time.Time
	Kind     EventKind
	JobIndex int
	Chapter  komiku.Chapter
	Page     int
	Pages    int
	Bytes    int64
	Result   Result
	Err      error
}

type Summary struct {
	Results     []Result
	Counts      map[Status]int
	PagesOK     int
	PagesFailed int
	Requested   int
	Started     int
	OutputDir   string
	AuditPath   string
	Cancelled   bool
	Err         error
}

type Engine struct {
	Client  *komiku.Client
	Store   *store.SeriesStore
	Workers int
	Now     func() time.Time
}

type Batch struct {
	events chan Event
	done   chan struct{}
	cancel context.CancelFunc
	client *komiku.Client
	now    func() time.Time

	eventMu     sync.Mutex
	sequence    uint64
	eventQueue  []Event
	eventHead   int
	eventNotify chan struct{}
	eventClosed bool

	pauseMu sync.Mutex
	paused  bool
	changed chan struct{}

	cancelOnce sync.Once
	summaryMu  sync.Mutex
	summary    Summary
}

func (e *Engine) Start(parent context.Context, jobs []Job) *Batch {
	now := e.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(parent)
	batch := &Batch{
		events:      make(chan Event, 128),
		done:        make(chan struct{}),
		cancel:      cancel,
		client:      e.Client,
		now:         now,
		changed:     make(chan struct{}),
		eventNotify: make(chan struct{}, 1),
	}
	go batch.dispatchEvents()
	go e.execute(ctx, batch, jobs)
	return batch
}

func (e *Engine) Run(ctx context.Context, jobs []Job) Summary {
	batch := e.Start(ctx, jobs)
	for range batch.Events() {
	}
	return batch.Wait()
}

func (b *Batch) Events() <-chan Event { return b.events }

func (b *Batch) Pause() {
	select {
	case <-b.done:
		return
	default:
	}
	b.pauseMu.Lock()
	if b.paused {
		b.pauseMu.Unlock()
		return
	}
	b.paused = true
	close(b.changed)
	b.changed = make(chan struct{})
	b.pauseMu.Unlock()
	if b.client != nil && b.client.Limiter != nil {
		b.client.Limiter.Pause()
	}
	b.emit(Event{Kind: BatchPaused})
}

func (b *Batch) Resume() {
	select {
	case <-b.done:
		return
	default:
	}
	b.pauseMu.Lock()
	if !b.paused {
		b.pauseMu.Unlock()
		return
	}
	b.paused = false
	close(b.changed)
	b.changed = make(chan struct{})
	b.pauseMu.Unlock()
	if b.client != nil && b.client.Limiter != nil {
		b.client.Limiter.Resume()
	}
	b.emit(Event{Kind: BatchResumed})
}

func (b *Batch) Cancel() {
	b.cancelOnce.Do(func() {
		b.emit(Event{Kind: BatchStopping})
		b.cancel()
	})
}

func (b *Batch) Wait() Summary {
	<-b.done
	b.summaryMu.Lock()
	defer b.summaryMu.Unlock()
	return b.summary
}

func (b *Batch) awaitResumed(ctx context.Context) error {
	for {
		b.pauseMu.Lock()
		if !b.paused {
			b.pauseMu.Unlock()
			return ctx.Err()
		}
		changed := b.changed
		b.pauseMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (b *Batch) emit(event Event) {
	b.eventMu.Lock()
	if b.eventClosed {
		b.eventMu.Unlock()
		return
	}
	b.sequence++
	event.Sequence = b.sequence
	event.At = b.now()
	b.eventQueue = append(b.eventQueue, event)
	b.eventMu.Unlock()
	select {
	case b.eventNotify <- struct{}{}:
	default:
	}
}

func (b *Batch) dispatchEvents() {
	defer close(b.events)
	for {
		b.eventMu.Lock()
		if b.eventHead < len(b.eventQueue) {
			event := b.eventQueue[b.eventHead]
			b.eventQueue[b.eventHead] = Event{}
			b.eventHead++
			if b.eventHead == len(b.eventQueue) {
				b.eventQueue = b.eventQueue[:0]
				b.eventHead = 0
			}
			b.eventMu.Unlock()
			b.events <- event
			continue
		}
		closed := b.eventClosed
		b.eventMu.Unlock()
		if closed {
			return
		}
		<-b.eventNotify
	}
}

func (b *Batch) closeEvents() {
	b.eventMu.Lock()
	b.eventClosed = true
	b.eventMu.Unlock()
	select {
	case b.eventNotify <- struct{}{}:
	default:
	}
}

func (e *Engine) execute(ctx context.Context, batch *Batch, jobs []Job) {
	defer batch.cancel()
	startedAt := batch.now()
	batch.emit(Event{Kind: BatchStarted})

	workers := e.Workers
	if workers <= 0 {
		workers = 3
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers < 0 {
		workers = 0
	}

	type indexedJob struct {
		index int
		job   Job
	}
	type indexedResult struct {
		index  int
		result Result
	}
	jobCh := make(chan indexedJob)
	resultCh := make(chan indexedResult, max(workers, 1))
	var workerGroup sync.WaitGroup
	var started atomic.Int64
	workerGroup.Add(workers)
	for range workers {
		go func() {
			defer workerGroup.Done()
			for item := range jobCh {
				started.Add(1)
				batch.emit(Event{Kind: ChapterStarted, JobIndex: item.index, Chapter: item.job.Chapter})
				result := e.downloadChapter(ctx, batch, item.index, item.job)
				batch.emit(Event{Kind: ChapterFinished, JobIndex: item.index, Chapter: item.job.Chapter, Result: result})
				resultCh <- indexedResult{index: item.index, result: result}
			}
		}()
	}
	go func() {
		defer close(jobCh)
		for index, job := range jobs {
			if err := batch.awaitResumed(ctx); err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case jobCh <- indexedJob{index: index, job: job}:
			}
		}
	}()
	go func() {
		workerGroup.Wait()
		close(resultCh)
	}()

	results := make([]Result, 0, len(jobs))
	for item := range resultCh {
		_ = item.index
		results = append(results, item.result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Chapter.Number == results[j].Chapter.Number {
			return results[i].Chapter.RawID < results[j].Chapter.RawID
		}
		return results[i].Chapter.Number < results[j].Chapter.Number
	})

	summary := Summary{
		Results:   results,
		Counts:    countStatuses(results),
		Requested: len(jobs),
		Started:   int(started.Load()),
		OutputDir: e.Store.Dir(),
		Cancelled: ctx.Err() != nil,
	}
	for _, result := range results {
		summary.PagesOK += result.Success
		summary.PagesFailed += result.Total - result.Success
	}
	logPath, err := WriteAuditLog(e.Store.Dir(), startedAt, results)
	if err != nil {
		summary.Err = err
	} else {
		summary.AuditPath = logPath
	}
	batch.summaryMu.Lock()
	batch.summary = summary
	batch.summaryMu.Unlock()
	batch.emit(Event{Kind: BatchFinished, Err: summary.Err})
	batch.closeEvents()
	close(batch.done)
}

func (e *Engine) downloadChapter(ctx context.Context, batch *Batch, jobIndex int, job Job) Result {
	chapter := job.Chapter
	rawDisambiguator := ""
	if job.Ambiguous {
		rawDisambiguator = chapter.RawID
	}
	if e.Store.IsDone(chapter.Number) {
		materialized, err := e.Store.MaterializeDone(chapter.Display, rawDisambiguator, job.Volume, job.Flat)
		if err != nil {
			return Result{Chapter: chapter, Status: Fail, Errors: []string{err.Error()}}
		}
		if materialized {
			dir, err := e.Store.ChapterDir(chapter.Display, rawDisambiguator, job.Volume, job.Flat)
			if err != nil {
				return Result{Chapter: chapter, Status: Fail, Errors: []string{err.Error()}}
			}
			pages, err := store.CountChapterPages(dir)
			if err != nil {
				return Result{Chapter: chapter, Status: Fail, Errors: []string{err.Error()}}
			}
			batch.emit(Event{Kind: ChapterPagesKnown, JobIndex: jobIndex, Chapter: chapter, Pages: pages})
			for page := 1; page <= pages; page++ {
				batch.emit(Event{Kind: PageSkipped, JobIndex: jobIndex, Chapter: chapter, Page: page, Pages: pages})
			}
			return Result{Chapter: chapter, Status: Done, Success: pages, Total: pages, SourceDir: dir}
		}
	}
	if err := batch.awaitResumed(ctx); err != nil {
		return interruptedResult(chapter, 0, 0, err)
	}
	if e.Client != nil && e.Client.Limiter != nil {
		if err := e.Client.Limiter.AwaitResumed(ctx); err != nil {
			return interruptedResult(chapter, 0, 0, err)
		}
	}
	images, err := e.Client.ChapterImages(ctx, chapter.URL)
	if err != nil {
		if ctx.Err() != nil {
			return interruptedResult(chapter, 0, 0, ctx.Err())
		}
		return Result{Chapter: chapter, Status: Fail, Errors: []string{err.Error()}}
	}
	if len(images) == 0 {
		return Result{Chapter: chapter, Status: NoImg}
	}
	batch.emit(Event{Kind: ChapterPagesKnown, JobIndex: jobIndex, Chapter: chapter, Pages: len(images)})
	dir, err := e.Store.ChapterDir(chapter.Display, rawDisambiguator, job.Volume, job.Flat)
	if err != nil {
		return Result{Chapter: chapter, Status: Fail, Total: len(images), Errors: []string{err.Error()}}
	}
	result := Result{Chapter: chapter, Total: len(images), SourceDir: dir, Errors: make([]string, 0)}
	for index, imageURL := range images {
		page := index + 1
		if _, valid := store.ExistingPage(dir, page); valid {
			result.Success++
			batch.emit(Event{Kind: PageSkipped, JobIndex: jobIndex, Chapter: chapter, Page: page, Pages: len(images)})
			continue
		}
		if err := batch.awaitResumed(ctx); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("page %03d: %v", page, err))
			break
		}
		batch.emit(Event{Kind: PageStarted, JobIndex: jobIndex, Chapter: chapter, Page: page, Pages: len(images)})
		var written int64
		err := e.Client.DownloadImage(ctx, imageURL, chapter.URL, func(extension string, body io.Reader) error {
			counter := &countingReader{reader: body}
			err := store.WritePage(dir, page, extension, counter)
			written = counter.count
			return err
		})
		if err != nil {
			message := fmt.Sprintf("page %03d: %v", page, err)
			result.Errors = append(result.Errors, message)
			batch.emit(Event{Kind: PageFailed, JobIndex: jobIndex, Chapter: chapter, Page: page, Pages: len(images), Err: err})
			if ctx.Err() != nil {
				break
			}
			continue
		}
		result.Success++
		batch.emit(Event{Kind: PageDone, JobIndex: jobIndex, Chapter: chapter, Page: page, Pages: len(images), Bytes: written})
	}
	if ctx.Err() != nil && result.Success < result.Total {
		result.Status = Part
		return result
	}
	switch {
	case result.Success == result.Total:
		result.Status = Done
		if !job.Ambiguous {
			if err := e.Store.MarkDone(chapter.Number); err != nil {
				result.Status = Part
				result.Errors = append(result.Errors, err.Error())
			}
		}
	case result.Success == 0:
		result.Status = Fail
	default:
		result.Status = Part
	}
	return result
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.count += int64(n)
	return n, err
}

func interruptedResult(chapter komiku.Chapter, success, total int, err error) Result {
	result := Result{Chapter: chapter, Status: Part, Success: success, Total: total}
	if err != nil {
		result.Errors = []string{err.Error()}
	}
	return result
}

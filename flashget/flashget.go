package flashget

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"code.cloudfoundry.org/bytefmt"

	"github.com/levigross/grequests"
	"github.com/qiniu/log"
)

const (
	STATUS_DOWNLOADING = "downloading"
	STATUS_SUCCESS     = "success"
	STATUS_FAILURE     = "failure"

	maxStoreSize = 3 << 30 // 3 GB
)

// ProxyWriter record download bytes
type ProxyWriter struct {
	W         io.Writer
	written   int
	createdAt time.Time
}

func NewProxyWriter(wr io.Writer) *ProxyWriter {
	return &ProxyWriter{
		W:         wr,
		createdAt: time.Now(),
	}
}

func (p *ProxyWriter) Write(data []byte) (n int, err error) {
	n, err = p.W.Write(data)
	p.written += n
	return
}

func (p *ProxyWriter) Written() int {
	return p.written
}

func (p *ProxyWriter) HumanSpeed() string {
	byteps := uint64(float64(p.written) / time.Since(p.createdAt).Seconds())
	return bytefmt.ByteSize(byteps) + "/s"
}

type Downloader struct {
	*ProxyWriter
	Filename      string    `json:"filename"`
	ContentLength int64     `json:"contentLength"`
	Status        string    `json:"status"`
	Description   string    `json:"description"`
	URL           string    `json:"url"`
	CreatedAt     time.Time `json:"createdAt"`
	FinishedAt    time.Time `json:"finishedAt"`
	AccessedAt    time.Time `json:"accessedAt"` // TODO(ssx): need to use some times
	wg            sync.WaitGroup
}

func (dl *Downloader) Wait() {
	dl.wg.Wait()
}

func (dl *Downloader) Written() int {
	if dl.ProxyWriter == nil {
		return 0
	}
	return dl.ProxyWriter.Written()
}

func (dl *Downloader) Finished() bool {
	return dl.Status == STATUS_SUCCESS
}

// record exists, but file are deleted
func (dl *Downloader) isFileRemoved(filename string) bool {
	if dl.Status == STATUS_DOWNLOADING {
		return false
	}
	_, err := os.Stat(filename)
	return err != nil
}

// Remove downloaded file
func (dl *Downloader) Remove() bool {
	// use file store in hash, just remove directory
	return os.Remove(dl.Filename) == nil
}

type DownloadManager struct {
	downloads map[string]*Downloader
	mu        sync.RWMutex
}

func NewDownloadManager() *DownloadManager {
	return &DownloadManager{
		downloads: make(map[string]*Downloader),
	}
}

// wr: is to record how many bytes copied
func (dm *DownloadManager) newDownloader(wr io.Writer) (dl *Downloader) {
	return &Downloader{
		ProxyWriter: NewProxyWriter(wr),
		CreatedAt:   time.Now(),
	}
}

func (dm *DownloadManager) Downloads() []*Downloader {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	dls := make([]*Downloader, 0, len(dm.downloads))
	for _, dl := range dm.downloads {
		dls = append(dls, dl)
	}
	// sort time reverse order
	sort.Slice(dls, func(i, j int) bool {
		return !dls[i].CreatedAt.Before(dls[j].CreatedAt)
	})
	return dls
}

// Remove download from manager
func (dm *DownloadManager) Remove(url string) bool {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dl, ok := dm.downloads[url]
	if !ok {
		return false
	}
	if !dl.Finished() { // TODO: need to support cancel download
		return false
	}
	if !dl.Remove() {
		return false
	}
	delete(dm.downloads, url)
	return true
}

// FinishedDownloads Ordered by FinishedAt
func (dm *DownloadManager) FinishedDownloads() []*Downloader {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	dls := make([]*Downloader, 0, len(dm.downloads))
	for _, dl := range dm.downloads {
		if dl.Finished() {
			dls = append(dls, dl)
		}
	}
	// sort time order
	sort.Slice(dls, func(i, j int) bool {
		return dls[i].FinishedAt.Before(dls[j].FinishedAt)
	})
	return dls
}

func (dm *DownloadManager) url2filename(url string) string {
	urlhash := hashStr(url)
	return urlhash + ".file"
}

func (dm *DownloadManager) locate(url string) *Downloader {
	filename := dm.url2filename(url)
	dl, ok := dm.downloads[url]
	if ok {
		if dl.isFileRemoved(filename) {
			return nil
		}
		return dl
	}

	if info, err := os.Stat(filename); err == nil {
		dm.downloads[url] = &Downloader{
			Filename:      filename,
			ContentLength: info.Size(),
			URL:           url,
			CreatedAt:     info.ModTime(),
			Status:        STATUS_SUCCESS,
		}
		return dm.downloads[url]
	}
	return nil
}

// Retrive url as local file
func (dm *DownloadManager) Retrive(url string) (dl *Downloader, err error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dl = dm.locate(url)
	if dl != nil {
		log.Infof("already download url: %s", url)
		return
	}

	// check if url valid
	log.Infof("check url: %s", url)
	resp, err := grequests.Get(url, nil)
	if err != nil {
		return nil, err
	}
	if !resp.Ok {
		resp.Close()
		return nil, fmt.Errorf("status: %d, body: %s", resp.StatusCode, resp.String())
	}

	// create download file
	log.Infof("create download %s", url)
	filename := dm.url2filename(url)
	tmpfilename := filename + ".cache"
	f, err := os.Create(tmpfilename)
	if err != nil {
		return nil, err
	}

	dl = dm.newDownloader(f)
	dl.URL = url
	dl.Filename = filename
	dl.Status = STATUS_DOWNLOADING
	fmt.Sscanf(resp.Header.Get("Content-Length"), "%d", &dl.ContentLength)
	dl.wg.Add(1)
	dm.downloads[url] = dl

	go func() {
		// defer resp.Body.Close()
		defer resp.Close()
		defer dl.wg.Done()
		defer func() { dl.FinishedAt = time.Now() }()
		nbytes, err := io.Copy(dl.ProxyWriter, resp)
		if err != nil {
			dl.Status = STATUS_FAILURE
			dl.Description = err.Error()
			f.Close()
			os.Remove(tmpfilename)
			log.Warnf("download failed, url: %s", url)
			return
		}
		if err = f.Close(); err != nil {
			dl.Status = STATUS_FAILURE
			dl.Description = "file close err: " + err.Error()
			return
		}
		if err = os.Rename(tmpfilename, filename); err != nil {
			dl.Status = STATUS_FAILURE
			dl.Description = "file rename err: " + err.Error()
			return
		}
		dl.Status = STATUS_SUCCESS
		log.Infof("download success, nbytes: %d, url: %s", nbytes, url)
	}()
	return dl, nil
}

func (dm *DownloadManager) EnableAutoRecycle() {
	// TODO(ssx): load already downloaded file
	filepath.Walk("./", func(path string, info os.FileInfo, err error) error {
		if filepath.Ext(info.Name()) == ".file" {
			log.Infof("remove %s", path)
			os.Remove(path)
		}
		return nil
	})

	go func() {
		for {
			log.Infof("recycle begins")
			dm.Recycle()
			time.Sleep(50 * time.Second) //time.Minute)
		}
	}()
}

// Recycle to free spaces
func (dm *DownloadManager) Recycle() {
	dls := dm.FinishedDownloads()
	// minStoreTime := time.Minute * 5

	var originSize int64 = 0
	for _, dl := range dls {
		originSize += dl.ContentLength
	}

	// remove old files
	var currentSize = originSize
	for _, dl := range dls {
		if currentSize < maxStoreSize {
			break
		}
		log.Infof("try to remove %s", dl.URL)
		if dm.Remove(dl.URL) {
			log.Infof("removed %s", dl.URL)
			currentSize -= dl.ContentLength
		}
	}
}

func hashStr(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	return hex.EncodeToString(h.Sum(nil))
}

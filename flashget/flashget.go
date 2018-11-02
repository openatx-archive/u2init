package flashget

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/levigross/grequests"
	"github.com/qiniu/log"
)

const (
	STATUS_DOWNLOADING = "downloading"
	STATUS_SUCCESS     = "success"
	STATUS_FAILURE     = "failure"
)

// ProxyWriter record download bytes
type ProxyWriter struct {
	W       io.Writer
	written int
}

func (p *ProxyWriter) Write(data []byte) (n int, err error) {
	n, err = p.W.Write(data)
	p.written += n
	return
}

func (p *ProxyWriter) Written() int {
	return p.written
}

type Downloader struct {
	*ProxyWriter
	Filename      string    `json:"filename"`
	ContentLength int       `json:"contentLength"`
	Status        string    `json:"status"`
	Description   string    `json:"description"`
	URL           string    `json:"url"`
	CreatedAt     time.Time `json:"createdAt"`
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
		ProxyWriter: &ProxyWriter{W: wr},
	}
}

func (dm *DownloadManager) Downloads() []*Downloader {
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
			ContentLength: int(info.Size()),
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
	dl.ContentLength, _ = strconv.Atoi(resp.Header.Get("Content-Length"))
	dl.wg.Add(1)
	dm.downloads[url] = dl

	go func() {
		// defer resp.Body.Close()
		defer resp.Close()
		defer dl.wg.Done()
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

func hashStr(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	return hex.EncodeToString(h.Sum(nil))
}

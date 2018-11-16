package flashget

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/cavaliercoder/grab"
	"github.com/qiniu/log"
)

const (
	STATUS_DOWNLOADING = "downloading"
	STATUS_SUCCESS     = "success"
	STATUS_FAILURE     = "failure"

	maxStoreSize = 3 << 30 // 3 GB
)

type Downloader struct {
	Filename      string    `json:"filename"`
	ContentLength int64     `json:"contentLength"`
	Status        string    `json:"status"`
	Description   string    `json:"description"`
	URL           string    `json:"url"`
	CreatedAt     time.Time `json:"createdAt"`
	FinishedAt    time.Time `json:"finishedAt"`
	AccessedAt    time.Time `json:"accessedAt"` // TODO(ssx): need to use some times
	wg            sync.WaitGroup
	resp          *grab.Response
}

func (dl *Downloader) Wait() {
	dl.wg.Wait()
}

func (dl *Downloader) Written() int64 {
	return dl.resp.BytesComplete()
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
	return os.Remove(dl.Filename) == nil
}

func (dl *Downloader) HumanSpeed() string {
	byteps := uint64(dl.resp.BytesPerSecond())
	return bytefmt.ByteSize(byteps) + "/s"
}

// DownloadManager manager all Downloaders
type DownloadManager struct {
	downloads map[string]*Downloader
	mu        sync.RWMutex
}

func NewDownloadManager() *DownloadManager {
	return &DownloadManager{
		downloads: make(map[string]*Downloader),
	}
}

func (dm *DownloadManager) newDownloader(resp *grab.Response) (dl *Downloader) {
	return &Downloader{
		resp:      resp,
		CreatedAt: time.Now(),
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
	// GET need to support ranges
	log.Infof("check url: %s", url)
	filename := dm.url2filename(url)
	tmpfilename := filename + ".cache"
	client := grab.NewClient()

	req, err := grab.NewRequest(tmpfilename, url)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Downloading %v...\n", req.URL())

	resp := client.Do(req)

	// create download file
	log.Infof("create download %s", url)

	// dl = dm.newDownloader(f)
	dl = dm.newDownloader(resp)
	dl.URL = url
	dl.Filename = filename
	dl.Status = STATUS_DOWNLOADING
	dl.ContentLength = resp.Size
	dl.wg.Add(1)
	dm.downloads[url] = dl

	go func() {
		defer dl.wg.Done()
		defer func() { dl.FinishedAt = time.Now() }()

		<-resp.Done

		if err := resp.Err(); err != nil {
			dl.Status = STATUS_FAILURE
			dl.Description = err.Error()
			os.Remove(tmpfilename)
			log.Warnf("download failed, url: %s", url)
			return
		}

		if err = os.Rename(tmpfilename, filename); err != nil {
			dl.Status = STATUS_FAILURE
			dl.Description = "file rename err: " + err.Error()
			return
		}
		dl.Status = STATUS_SUCCESS
		log.Infof("download save to %v", resp.Filename)
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
			// log.Infof("recycle begins")
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

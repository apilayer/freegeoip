// Copyright 2009 The freegeoip authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package freegeoip

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"log"

	"github.com/howeyc/fsnotify"
	"github.com/oschwald/maxminddb-golang"
)

var (
	// ErrUnavailable may be returned by DB.Lookup when the database
	// points to a URL and is not yet available because it's being
	// downloaded in background.
	ErrUnavailable = errors.New("no database available")

	// Local cached copy of a database downloaded from a URL.
	defaultDB = filepath.Join(os.TempDir(), "freegeoip", "db")
	defaultArchive = filepath.Join(os.TempDir(), "freegeoip", "db.gz")
)

// DB is the IP geolocation database.
type DB struct {
	file        string            // Database file name.
	reader      *maxminddb.Reader // Actual db object.
	notifyQuit  chan struct{}     // Stop auto-update and watch goroutines.
	notifyOpen  chan string       // Notify when a db file is open.
	notifyError chan error        // Notify when an error occurs.
	notifyInfo  chan string       // Notify random actions for logging
	closed      bool              // Mark this db as closed.
	lastUpdated time.Time         // Last time the db was updated.
	mu          sync.RWMutex      // Protects all the above.

	updateInterval   time.Duration // Update interval.
	maxRetryInterval time.Duration // Max retry interval in case of failure.
}

// Open creates and initializes a DB from a local file.
//
// The database file is monitored by fsnotify and automatically
// reloads when the file is updated or overwritten.
func Open(dsn string) (*DB, error) {
	d := &DB{
		file:        dsn,
		notifyQuit:  make(chan struct{}),
		notifyOpen:  make(chan string, 1),
		notifyError: make(chan error, 1),
		notifyInfo:  make(chan string, 1),
	}
	err := d.openFile()
	if err != nil {
		d.Close()
		return nil, err
	}
	err = d.watchFile()
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("fsnotify failed for %s: %s", dsn, err)
	}
	return d, nil
}

// MaxMindUpdateURL generates the URL for MaxMind paid databases.
func MaxMindUpdateURL(hostname, productID, licenseKey string) (string, error) {
	baseurl := "https://" + hostname + "/app/"
	// Get the file name for the product ID.
	u := baseurl + "geoip_download?edition_id=" + productID + "&date=&license_key=" + licenseKey + "&suffix=tar.gz"
	return u, nil
}

// OpenURL creates and initializes a DB from a URL.
// It automatically downloads and updates the file in background, and
// keeps a local copy on $TMPDIR.
func OpenURL(url string, updateInterval, maxRetryInterval time.Duration) (*DB, error) {
	d := &DB{
		file:             defaultDB,
		notifyQuit:       make(chan struct{}),
		notifyOpen:       make(chan string, 1),
		notifyError:      make(chan error, 1),
		notifyInfo:       make(chan string, 1),
		updateInterval:   updateInterval,
		maxRetryInterval: maxRetryInterval,
	}
	d.openFile() // Optional, might fail.
	go d.autoUpdate(url)
	err := d.watchFile()
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("fsnotify failed for %s: %s", d.file, err)
	}
	return d, nil
}

func (d *DB) watchFile() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	dbdir, err := d.makeDir()
	if err != nil {
		return err
	}
	go d.watchEvents(watcher)
	return watcher.Watch(dbdir)
}

func (d *DB) watchEvents(watcher *fsnotify.Watcher) {
	for {
		select {
		case ev := <-watcher.Event:
			if ev.Name == d.file && (ev.IsCreate() || ev.IsModify()) {
				d.openFile()
			}
		case <-watcher.Error:
		case <-d.notifyQuit:
			watcher.Close()
			return
		}
		time.Sleep(time.Second) // Suppress high-rate events.
	}
}

func (d *DB) openFile() error {
	_, err := d.ProcessFile()
	if err != nil {
		return err
	}
	reader, err := d.newReader(d.file)
	if err != nil {
		return err
	}
	stat, err := os.Stat(d.file)
	if err != nil {
		return err
	}
	d.setReader(reader, stat.ModTime())
	return nil
}

func (d *DB) newReader(dbfile string) (*maxminddb.Reader, error) {
	return maxminddb.Open(dbfile)
}

func (d *DB) setReader(reader *maxminddb.Reader, modtime time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		reader.Close()
		return
	}
	if d.reader != nil {
		d.reader.Close()
	}
	d.reader = reader
	d.lastUpdated = modtime.UTC()
	select {
	case d.notifyOpen <- d.file:
	default:
	}
}

func (d *DB) autoUpdate(url string) {
	backoff := time.Second
	for {
		d.sendInfo("starting update")
		err := d.runUpdate(url)
		if err != nil {
			bs := backoff.Seconds()
			ms := d.maxRetryInterval.Seconds()
			backoff = time.Duration(math.Min(bs*math.E, ms)) * time.Second
			d.sendError(fmt.Errorf("download failed (will retry in %s): %s", backoff, err))
		} else {
			backoff = d.updateInterval
		}
		d.sendInfo("finished update")
		select {
		case <-d.notifyQuit:
			return
		case <-time.After(backoff):
			// Sleep till time for the next update attempt.
		}
	}
}

func (d *DB) runUpdate(url string) error {
	yes, err := d.needUpdate(url)
	if err != nil {
		return err
	}
	if !yes {
		return nil
	}
	tmpfile, err := d.download(url)
	if err != nil {
		return err
	}
	err = d.renameFile(tmpfile)
	if err != nil {
		// Cleanup the tempfile if renaming failed.
		os.RemoveAll(tmpfile)
	}
	return err
}

func (d *DB) needUpdate(url string) (bool, error) {
	stat, err := os.Stat(defaultArchive)
	if err != nil {
		return true, nil // Local db is missing, must be downloaded.
	}

	resp, err := http.Head(url)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	LastModified := resp.Header.Get("Last-Modified")
	if LastModified != "" {
		t, err := time.Parse(http.TimeFormat, LastModified)
		if err != nil {
			return false, err
		}
		if t.After(stat.ModTime()) {
			return true, nil
		}
	}

	if stat.Size() != resp.ContentLength {
		return true, nil
	}
	return false, nil
}

func (d *DB) download(url string) (tmpfile string, err error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	tmpfile = filepath.Join(os.TempDir(),
		fmt.Sprintf("_freegeoip.%d.db.gz", time.Now().UnixNano()))
	f, err := os.Create(tmpfile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return "", err
	}
	return tmpfile, nil
}

func (d *DB) makeDir() (dbdir string, err error) {
	dbdir = filepath.Dir(d.file)
	_, err = os.Stat(dbdir)
	if err != nil {
		err = os.MkdirAll(dbdir, 0755)
		if err != nil {
			return "", err
		}
	}
	return dbdir, nil
}

func (d *DB) renameFile(name string) error {
	os.Rename(d.file, d.file+".bak") // Optional, might fail.
	_, err := d.makeDir()
	if err != nil {
		return err
	}
	return os.Rename(name, d.file)
}

// Date returns the UTC date the database file was last modified.
// If no database file has been opened the behaviour of Date is undefined.
func (d *DB) Date() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastUpdated
}

// NotifyClose returns a channel that is closed when the database is closed.
func (d *DB) NotifyClose() <-chan struct{} {
	return d.notifyQuit
}

// NotifyOpen returns a channel that notifies when a new database is
// loaded or reloaded. This can be used to monitor background updates
// when the DB points to a URL.
func (d *DB) NotifyOpen() (filename <-chan string) {
	return d.notifyOpen
}

// NotifyError returns a channel that notifies when an error occurs
// while downloading or reloading a DB that points to a URL.
func (d *DB) NotifyError() (errChan <-chan error) {
	return d.notifyError
}

// NotifyInfo returns a channel that notifies informational messages
// while downloading or reloading.
func (d *DB) NotifyInfo() <-chan string {
	return d.notifyInfo
}

func (d *DB) sendError(err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return
	}
	select {
	case d.notifyError <- err:
	default:
	}
}

func (d *DB) sendInfo(message string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return
	}
	select {
	case d.notifyInfo <- message:
	default:
	}
}

// Lookup performs a database lookup of the given IP address, and stores
// the response into the result value. The result value must be a struct
// with specific fields and tags as described here:
// https://godoc.org/github.com/oschwald/maxminddb-golang#Reader.Lookup
//
// See the DefaultQuery for an example of the result struct.
func (d *DB) Lookup(addr net.IP, result interface{}) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.reader != nil {
		return d.reader.Lookup(addr, result)
	}
	return ErrUnavailable
}

// DefaultQuery is the default query used for database lookups.
type DefaultQuery struct {
	Continent struct {
		Code string `maxminddb:"code"`
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"continent"`
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Region []struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
		MetroCode uint    `maxminddb:"metro_code"`
		TimeZone  string  `maxminddb:"time_zone"`
		AccuracyRadius  uint  `maxminddb:"accuracy_radius"`
	} `maxminddb:"location"`
	Postal struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"postal"`
}

// Close closes the database.
func (d *DB) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.closed = true
		close(d.notifyQuit)
		close(d.notifyOpen)
		close(d.notifyError)
		close(d.notifyInfo)
	}
	if d.reader != nil {
		d.reader.Close()
		d.reader = nil
	}
}

func (d *DB) ProcessFile() (string, error) {
	f, err := os.Open(defaultArchive)
	if err != nil {
		return "", err
	}
	defer f.Close()

	err, _ = d.ExtractTarGz(f)
	if err != nil {
		return "", err
	}
	return d.file, nil
}


func (d *DB) ExtractTarGz(gzipStream io.Reader) (error, string) {
	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		return err, ""
	}

	tarReader := tar.NewReader(uncompressedStream)

	for true {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return err, ""
		}

		switch header.Typeflag {
		case tar.TypeDir:
			break;
		case tar.TypeReg:
			if strings.Contains(header.Name, "mmdb") || strings.Contains(header.Name, "BIN") {
				outFile, err := os.Create(d.file)
				if err != nil {
					return err, ""
				}
				defer outFile.Close()
				if _, err := io.Copy(outFile, tarReader); err != nil {
					return err, ""
				}else{
					return nil, d.file
				}
			}
		default:
			log.Fatalf("ExtractTarGz: uknown type: %b in %s", header.Typeflag, header.Name)
		}
	}

	return nil, ""
}
// Copyright (C) 2010, Kyle Lemons <kyle@kylelemons.net>.  All rights reserved.

package log4go

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"time"
)

// This log writer sends output to a file
type FileLogWriter struct {
	rec chan *LogRecord
	rot chan bool

	// The opened file
	filename string
	file     *os.File

	// The logging format
	format string

	// File header/trailer
	header, trailer string

	// Rotate at linecount
	maxlines          int
	maxlines_curlines int

	// Rotate at size
	maxsize         int
	maxsize_cursize int

	// Rotate daily
	daily          bool
	daily_opendate int

	// Keep old logfiles (.001, .002, etc)
	rotate    bool
	maxbackup int
	logindex  int
	//compresslog []string
}

// This is the FileLogWriter's output method
func (w *FileLogWriter) LogWrite(rec *LogRecord) {
	w.rec <- rec
}

func (w *FileLogWriter) Close() {
	close(w.rec)
	w.file.Sync()
}

// NewFileLogWriter creates a new LogWriter which writes to the given file and
// has rotation enabled if rotate is true.
//
// If rotate is true, any time a new log file is opened, the old one is renamed
// with a .### extension to preserve it.  The various Set* methods can be used
// to configure log rotation based on lines, size, and daily.
//
// The standard log-line format is:
//   [%D %T] [%L] (%S) %M
func NewFileLogWriter(fname string, rotate bool) *FileLogWriter {
	w := &FileLogWriter{
		rec:            make(chan *LogRecord, LogBufferLength),
		rot:            make(chan bool),
		filename:       fname,
		format:         "[%D %T] [%L] (%S) %M",
		daily_opendate: -1,
		rotate:         rotate,
		maxbackup:      999,
		logindex:       0,
	}

	// open the file for the first time
	if err := w.intRotate(); err != nil {
		fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
		return nil
	}

	go func() {
		defer func() {
			if w.file != nil {
				fmt.Fprint(w.file, FormatLogRecord(w.trailer, &LogRecord{Created: time.Now()}))
				w.file.Close()
			}
		}()

		for {
			select {
			case <-w.rot:
				if err := w.intRotate(); err != nil {
					fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
					continue
				}
			case rec, ok := <-w.rec:
				if !ok {
					return
				}
				now := time.Now()
				if (w.maxlines > 0 && w.maxlines_curlines >= w.maxlines) ||
					(w.maxsize > 0 && w.maxsize_cursize >= w.maxsize) ||
					(w.daily && now.Day() != w.daily_opendate) {
					if err := w.intRotate(); err != nil {
						fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
						continue
					}
				}

				// Perform the write
				n, err := fmt.Fprint(w.file, FormatLogRecord(w.format, rec))
				if err != nil {
					fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
					continue
				}

				// Update the counts
				w.maxlines_curlines++
				w.maxsize_cursize += n
			}
		}
	}()

	return w
}

// Request that the logs rotate
func (w *FileLogWriter) Rotate() {
	w.rot <- true
}

// If this is called in a threaded context, it MUST be synchronized
func (w *FileLogWriter) intRotate() error {
	// Close any log file that may be open
	if w.file != nil {
		fmt.Fprint(w.file, FormatLogRecord(w.trailer, &LogRecord{Created: time.Now()}))
		w.file.Close()
	}

	now := time.Now()
	if (w.daily_opendate != -1) && (now.Day() != w.daily_opendate) {
		//获取日志目录
		index := strings.LastIndex(w.filename, string(os.PathSeparator))
		if index != -1 {
			dir := w.filename[:index+1]
			fmt.Fprintf(os.Stderr, "tarLogFile %v %v\n", w.filename, dir)
			//遍历当前目录下的所有文件
			// 获取 dir 下的文件或子目录列表
			fis, er := ioutil.ReadDir(dir)
			if er == nil {
				var files []string
				// 开始遍历
				for _, fi := range fis {
					if !fi.IsDir() {
						files = append(files, dir+fi.Name())
					}
				}
				go w.tarLogFile(files, dir)
			} else {
				fmt.Fprintf(os.Stderr, "read dir:%s failed,%s\n", dir, er.Error())
			}
		} else {
			fmt.Fprintf(os.Stderr, "w.filename:%s failed\n", w.filename)
		}
	}
	nfilename := w.filename

	// If we are keeping log files, move it to the next available number
	if w.rotate {
		w.logindex++
		nfilename = w.filename + fmt.Sprintf("_%03d", w.logindex)
	}
	nfilename = nfilename + ".log"
	// Open the log file
	fd, err := os.OpenFile(nfilename, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0664)
	if err != nil {
		fmt.Fprintf(os.Stderr, "OpenFile failed: %s\n", err.Error())
		return err
	}
	w.file = fd

	fmt.Fprint(w.file, FormatLogRecord(w.header, &LogRecord{Created: now}))

	// Set the daily open date to the current date
	w.daily_opendate = now.Day()

	// initialize rotation values
	w.maxlines_curlines = 0
	w.maxsize_cursize = 0

	return nil
}

// Set the logging format (chainable).  Must be called before the first log
// message is written.
func (w *FileLogWriter) SetFormat(format string) *FileLogWriter {
	w.format = format
	return w
}

// Set the logfile header and footer (chainable).  Must be called before the first log
// message is written.  These are formatted similar to the FormatLogRecord (e.g.
// you can use %D and %T in your header/footer for date and time).
func (w *FileLogWriter) SetHeadFoot(head, foot string) *FileLogWriter {
	w.header, w.trailer = head, foot
	if w.maxlines_curlines == 0 {
		fmt.Fprint(w.file, FormatLogRecord(w.header, &LogRecord{Created: time.Now()}))
	}
	return w
}

// Set rotate at linecount (chainable). Must be called before the first log
// message is written.
func (w *FileLogWriter) SetRotateLines(maxlines int) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateLines: %v\n", maxlines)
	w.maxlines = maxlines
	return w
}

// Set rotate at size (chainable). Must be called before the first log message
// is written.
func (w *FileLogWriter) SetRotateSize(maxsize int) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateSize: %v\n", maxsize)
	w.maxsize = maxsize
	return w
}

// Set rotate daily (chainable). Must be called before the first log message is
// written.
func (w *FileLogWriter) SetRotateDaily(daily bool) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateDaily: %v\n", daily)
	w.daily = daily
	return w
}

// Set max backup files. Must be called before the first log message
// is written.
func (w *FileLogWriter) SetRotateMaxBackup(maxbackup int) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateMaxBackup: %v\n", maxbackup)
	w.maxbackup = maxbackup
	return w
}

// SetRotate changes whether or not the old logs are kept. (chainable) Must be
// called before the first log message is written.  If rotate is false, the
// files are overwritten; otherwise, they are rotated to another file before the
// new log is opened.
func (w *FileLogWriter) SetRotate(rotate bool) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotate: %v\n", rotate)
	w.rotate = rotate
	return w
}

func (w *FileLogWriter) tarLogFile(files []string, dir string) {
	fmt.Fprintf(os.Stderr, "tarLogFile %v \n", w.filename)
	os.Mkdir(dir+"backup", os.ModePerm) //在当前目录下生成md目录

	destfile := dir + "backup" + string(os.PathSeparator) + time.Now().AddDate(0, 0, -1).Format("2006-01-02-15-04") + ".tar.gz"
	fmt.Fprintf(os.Stderr, "tarLogFile files: %v %v\n", files, destfile)

	if err := Compress(files, destfile); err != nil {
		fmt.Fprintf(os.Stderr, "tarLogFile Compress:%s\n", err.Error())
		return
	}

	//删除压缩过的文件
	for _, file := range files {
		os.Remove(file)
	}
	dd := dir + "backup" + string(os.PathSeparator)
	cc, er := ioutil.ReadDir(dd)
	if er != nil {
		fmt.Fprintf(os.Stderr, "ReadDir %v\n", er)
		return
	}
	nt := time.Now().Unix()
	for _, ff := range cc {
		if !ff.IsDir() {
			if ff.ModTime().Unix()+int64(w.maxbackup*24*3600) < nt {
				os.Remove(dd + ff.Name())
			}
		}
	}
	return
}

//压缩 使用gzip压缩成tar.gz
func Compress(files []string, dest string) error {
	_, err := os.Stat(dest)
	if err == nil || os.IsExist(err) {
		return errors.New("file have exist")
	}

	d, _ := os.Create(dest)
	defer d.Close()
	gw := gzip.NewWriter(d)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()
	for _, file := range files {
		// 打开要打包的文件，准备读取
		fr, err := os.Open(file)
		if err != nil {
			return err
		}
		defer fr.Close()

		err = compress(fr, "", tw)
		if err != nil {
			return err
		}
	}
	return nil
}

func compress(file *os.File, prefix string, tw *tar.Writer) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		prefix = prefix + "/" + info.Name()
		fileInfos, err := file.Readdir(-1)
		if err != nil {
			return err
		}
		for _, fi := range fileInfos {
			f, err := os.Open(file.Name() + "/" + fi.Name())
			if err != nil {
				return err
			}
			err = compress(f, prefix, tw)
			if err != nil {
				return err
			}
		}
	} else {
		header, err := tar.FileInfoHeader(info, "")
		header.Name = prefix + "/" + header.Name
		if err != nil {
			return err
		}
		err = tw.WriteHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, file)
		file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// NewXMLLogWriter is a utility method for creating a FileLogWriter set up to
// output XML record log messages instead of line-based ones.
func NewXMLLogWriter(fname string, rotate bool) *FileLogWriter {
	return NewFileLogWriter(fname, rotate).SetFormat(
		`	<record level="%L">
		<timestamp>%D %T</timestamp>
		<source>%S</source>
		<message>%M</message>
	</record>`).SetHeadFoot("<log created=\"%D %T\">", "</log>")
}

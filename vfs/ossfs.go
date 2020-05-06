package vfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/drakkan/sftpgo/logger"
	"github.com/drakkan/sftpgo/metrics"
	"github.com/eikenb/pipeat"
)

// OSSFsConfig defines the configuration for OSS based filesystem
type OSSFsConfig struct {
	Bucket string `json:"bucket,omitempty"`
	// KeyPrefix is similar to a chroot directory for local filesystem.
	// If specified the SFTP user will only see objects that starts with
	// this prefix and so you can restrict access to a specific virtual
	// folder. The prefix, if not empty, must not start with "/" and must
	// end with "/".
	// If empty the whole bucket contents will be available
	KeyPrefix string `json:"key_prefix,omitempty"`
	//Region       string `json:"region,omitempty"`
	AccessKey    string `json:"access_key,omitempty"`
	AccessSecret string `json:"access_secret,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	//StorageClass string `json:"storage_class,omitempty"`
	// The buffer size (in MB) to use for multipart uploads. The minimum allowed part size is 5MB,
	// and if this value is set to zero, the default value (5MB) for the AWS SDK will be used.
	// The minimum allowed value is 5.
	// Please note that if the upload bandwidth between the SFTP client and SFTPGo is greater than
	// the upload bandwidth between SFTPGo and S3 then the SFTP client have to wait for the upload
	// of the last parts to S3 after it ends the file upload to SFTPGo, and it may time out.
	// Keep this in mind if you customize these parameters.
	UploadPartSize int64 `json:"upload_part_size,omitempty"`
	// How many parts are uploaded in parallel
	//UploadConcurrency int `json:"upload_concurrency,omitempty"`
}

// OSSFs is a Fs implementation for Amazon S3 compatible object storage.
type OSSFs struct {
	connectionID   string
	localTempDir   string
	config         OSSFsConfig
	svc            *oss.Client
	ctxTimeout     time.Duration
	ctxLongTimeout time.Duration
}

// NewOSSFs returns an OSSFs object that allows to interact with an oss compatible
// object storage
func NewOSSFs(connectionID, localTempDir string, config OSSFsConfig) (Fs, error) {
	fs := OSSFs{
		connectionID:   connectionID,
		localTempDir:   localTempDir,
		config:         config,
		ctxTimeout:     30 * time.Second,
		ctxLongTimeout: 300 * time.Second,
	}
	if err := ValidateOSSFsConfig(&fs.config); err != nil {
		return fs, err
	}

	if fs.config.UploadPartSize == 0 {
		fs.config.UploadPartSize = s3manager.DefaultUploadPartSize
	} else {
		fs.config.UploadPartSize *= 1024 * 1024
	}

	var err error
	fs.svc, err = oss.New(config.Endpoint, config.AccessKey, config.AccessSecret)
	return fs, err
}

// Name returns the name for the Fs implementation
func (fs OSSFs) Name() string {
	return fmt.Sprintf("OSSFs bucket: %#v", fs.config.Bucket)
}

// ConnectionID returns the SSH connection ID associated to this Fs implementation
func (fs OSSFs) ConnectionID() string {
	return fs.connectionID
}

// Stat returns a FileInfo describing the named file
func (fs OSSFs) Stat(name string) (os.FileInfo, error) {
	var result FileInfo
	if name == "/" || name == "." {
		err := fs.checkIfBucketExists()
		if err != nil {
			return result, err
		}
		return NewFileInfo(name, true, 0, time.Time{}), nil
	}
	if "/"+fs.config.KeyPrefix == name+"/" {
		return NewFileInfo(name, true, 0, time.Time{}), nil
	}
	prefix := path.Dir(name)
	if prefix == "/" || prefix == "." {
		prefix = ""
	} else {
		prefix = strings.TrimPrefix(prefix, "/")
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
	}
	bucket, err := fs.svc.Bucket(fs.config.Bucket)
	if err != nil {
		return result, err
	}
	page, err := bucket.ListObjects(oss.Prefix(prefix), oss.Delimiter("/"))
	if err == nil && len(result.Name()) == 0 {
		err = errors.New("404 no such file or directory")
	}
	for _, p := range page.CommonPrefixes {
		if fs.isEqual(&p, name) {
			result = NewFileInfo(name, true, 0, time.Time{})
		}
	}
	for _, fileObject := range page.Objects {
		if fs.isEqual(&fileObject.Key, name) {
			objectSize := fileObject.Size
			objectModTime := fileObject.LastModified
			isDir := strings.HasSuffix(fileObject.Key, "/")
			result = NewFileInfo(name, isDir, objectSize, objectModTime)
		}
	}

	metrics.OSSListObjectsCompleted(err)
	if len(result.Name()) == 0 {
		err = errors.New("404 no such file or directory")
	}
	return result, err
}

// Lstat returns a FileInfo describing the named file
func (fs OSSFs) Lstat(name string) (os.FileInfo, error) {
	return fs.Stat(name)
}

// Open opens the named file for reading
func (fs OSSFs) Open(name string) (*os.File, *pipeat.PipeReaderAt, func(), error) {
	/*r, w, err := pipeat.AsyncWriterPipeInDir(fs.localTempDir)
	if err != nil {
		return nil, nil, nil, err
	}*/

	bucket, err := fs.svc.Bucket(fs.config.Bucket)
	if err != nil {
		return nil, nil, nil, err
	}
	go func() {
		key := name
		err = bucket.DownloadFile(key, name, 100*1024, oss.Checkpoint(true, name+".checkpoint"), oss.CheckpointDir(true, fs.localTempDir))
		if err != nil {
			return
		}
		fsLog(fs, logger.LevelDebug, "download completed, path: %#v size: %v, err: %v", name, 0, err)
		metrics.S3TransferCompleted(0, 1, err)
	}()
	/*
		downloader := s3manager.NewDownloaderWithClient(fs.svc)
		go func() {
			defer cancelFn()
			key := name
			n, err := downloader.DownloadWithContext(ctx, w, &s3.GetObjectInput{
				Bucket: aws.String(fs.config.Bucket),
				Key:    aws.String(key),
			})
			w.CloseWithError(err) //nolint:errcheck // the returned error is always null
			fsLog(fs, logger.LevelDebug, "download completed, path: %#v size: %v, err: %v", name, n, err)
			metrics.S3TransferCompleted(n, 1, err)
		}()*/
	return nil, nil, nil, nil
	//return nil, r, cancelFn, nil
}

// Create creates or opens the named file for writing
func (fs OSSFs) Create(name string, flag int) (*os.File, *pipeat.PipeWriterAt, func(), error) {
	bucket, err := fs.svc.Bucket(fs.config.Bucket)
	if err != nil {
		return nil, nil, nil, err
	}

	/*r, w, err := pipeat.PipeInDir(fs.localTempDir)
	if err != nil {
		return nil, nil, nil, err
	}*/
	go func() {
		key := name
		chunks, err := oss.SplitFileByPartSize(key, fs.config.UploadPartSize)
		if err != nil {
			return
		}
		imur, err := bucket.InitiateMultipartUpload(key)
		if err != nil {
			return
		}
		for _, chunk := range chunks {
			_, err := bucket.UploadPartFromFile(imur, fs.localTempDir+name, chunk.Offset, chunk.Size, chunk.Number)
			if err != nil {
				break
			}
		}

		fsLog(fs, logger.LevelDebug, "upload completed, path: %#v, response: %v, readed bytes: %v, err: %+v",
			name, "response", 0, err)
		metrics.S3TransferCompleted(0, 0, err)
	}()
	return nil, nil, nil, nil
}

// Rename renames (moves) source to target.
// We don't support renaming non empty directories since we should
// rename all the contents too and this could take long time: think
// about directories with thousands of files, for each file we should
// execute a CopyObject call.
// TODO: rename does not work for files bigger than 5GB, implement
// multipart copy or wait for this pull request to be merged:
//
// https://github.com/aws/aws-sdk-go/pull/2653
//
func (fs OSSFs) Rename(source, target string) error {
	if source == target {
		return nil
	}
	fi, err := fs.Stat(source)
	if err != nil {
		return err
	}
	copySource := fs.Join(fs.config.Bucket, source)
	if fi.IsDir() {
		contents, err := fs.ReadDir(source)
		if err != nil {
			return err
		}
		if len(contents) > 0 {
			return fmt.Errorf("Cannot rename non empty directory: %#v", source)
		}
		if !strings.HasSuffix(copySource, "/") {
			copySource += "/"
		}
		if !strings.HasSuffix(target, "/") {
			target += "/"
		}
	}
	bucket, err := fs.svc.Bucket(fs.config.Bucket)
	if err != nil {
		return err
	}
	_, err = bucket.CopyObject(copySource, target)
	metrics.OSSCopyObjectCompleted(err)
	if err != nil {
		return err
	}
	return fs.Remove(source, fi.IsDir())
}

// Remove removes the named file or (empty) directory.
func (fs OSSFs) Remove(name string, isDir bool) error {
	if isDir {
		contents, err := fs.ReadDir(name)
		if err != nil {
			return err
		}
		if len(contents) > 0 {
			return fmt.Errorf("Cannot remove non empty directory: %#v", name)
		}
		if !strings.HasSuffix(name, "/") {
			name += "/"
		}
	}
	bucket, err := fs.svc.Bucket(fs.config.Bucket)
	if err != nil {
		return err
	}
	err = bucket.DeleteObject(name)
	metrics.OSSDeleteObjectCompleted(err)
	return err
}

// Mkdir creates a new directory with the specified name and default permissions
func (fs OSSFs) Mkdir(name string) error {
	_, err := fs.Stat(name)
	if !fs.IsNotExist(err) {
		return err
	}
	if !strings.HasSuffix(name, "/") {
		name += "/"
	}
	_, w, _, err := fs.Create(name, 0)
	if err != nil {
		return err
	}
	return w.Close()
}

// Symlink creates source as a symbolic link to target.
func (OSSFs) Symlink(source, target string) error {
	return errors.New("403 symlinks are not supported")
}

// Chown changes the numeric uid and gid of the named file.
// Silently ignored.
func (OSSFs) Chown(name string, uid int, gid int) error {
	return nil
}

// Chmod changes the mode of the named file to mode.
// Silently ignored.
func (OSSFs) Chmod(name string, mode os.FileMode) error {
	return nil
}

// Chtimes changes the access and modification times of the named file.
// Silently ignored.
func (OSSFs) Chtimes(name string, atime, mtime time.Time) error {
	return errors.New("403 chtimes is not supported")
}

// ReadDir reads the directory named by dirname and returns
// a list of directory entries.
func (fs OSSFs) ReadDir(dirname string) ([]os.FileInfo, error) {
	var result []os.FileInfo
	// dirname must be already cleaned
	prefix := ""
	if dirname != "/" && dirname != "." {
		prefix = strings.TrimPrefix(dirname, "/")
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
	}
	bucket, err := fs.svc.Bucket(fs.config.Bucket)
	if err != nil {
		return result, err
	}
	page, err := bucket.ListObjects(oss.Prefix(prefix), oss.Delimiter("/"))
	if err != nil {
		return result, err
	}
	for _, p := range page.CommonPrefixes {
		name, isDir := fs.resolve(&p, prefix)
		result = append(result, NewFileInfo(name, isDir, 0, time.Time{}))
	}
	for _, fileObject := range page.Objects {
		objectSize := fileObject.Size
		objectModTime := fileObject.LastModified
		name, isDir := fs.resolve(&fileObject.Key, prefix)
		if len(name) == 0 {
			continue
		}
		result = append(result, NewFileInfo(name, isDir, objectSize, objectModTime))
	}
	metrics.OSSListObjectsCompleted(err)
	return result, err
}

// IsUploadResumeSupported returns true if upload resume is supported.
// SFTP Resume is not supported on OSS
func (OSSFs) IsUploadResumeSupported() bool {
	return false
}

// IsAtomicUploadSupported returns true if atomic upload is supported.
// OSS uploads are already atomic, we don't need to upload to a temporary
// file
func (OSSFs) IsAtomicUploadSupported() bool {
	return false
}

// IsNotExist returns a boolean indicating whether the error is known to
// report that a file or directory does not exist
func (OSSFs) IsNotExist(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "404")
}

// IsPermission returns a boolean indicating whether the error is known to
// report that permission is denied.
func (OSSFs) IsPermission(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "403")
}

// CheckRootPath creates the specified root directory if it does not exists
func (fs OSSFs) CheckRootPath(username string, uid int, gid int) bool {
	// we need a local directory for temporary files
	osFs := NewOsFs(fs.ConnectionID(), fs.localTempDir, nil)
	osFs.CheckRootPath(username, uid, gid)
	return fs.checkIfBucketExists() != nil
}

// ScanRootDirContents returns the number of files contained in the bucket,
// and their size
func (fs OSSFs) ScanRootDirContents() (int, int64, error) {
	numFiles := 0
	size := int64(0)
	bucket, err := fs.svc.Bucket(fs.config.Bucket)
	if err != nil {
		return numFiles, size, err
	}
	page, err := bucket.ListObjects(oss.Prefix(fs.config.KeyPrefix))
	if err != nil {
		return numFiles, size, err
	}
	for _, fileObject := range page.Objects {
		numFiles++
		size += fileObject.Size
	}
	metrics.OSSListObjectsCompleted(err)
	return numFiles, size, err
}

// GetAtomicUploadPath returns the path to use for an atomic upload.
// S3 uploads are already atomic, we never call this method for S3
func (OSSFs) GetAtomicUploadPath(name string) string {
	return ""
}

// GetRelativePath returns the path for a file relative to the user's home dir.
// This is the path as seen by SFTP users
func (fs OSSFs) GetRelativePath(name string) string {
	rel := path.Clean(name)
	if rel == "." {
		rel = ""
	}
	if !strings.HasPrefix(rel, "/") {
		return "/" + rel
	}
	if len(fs.config.KeyPrefix) > 0 {
		if !strings.HasPrefix(rel, "/"+fs.config.KeyPrefix) {
			rel = "/"
		}
		rel = path.Clean("/" + strings.TrimPrefix(rel, "/"+fs.config.KeyPrefix))
	}
	return rel
}

// Join joins any number of path elements into a single path
func (OSSFs) Join(elem ...string) string {
	return path.Join(elem...)
}

// ResolvePath returns the matching filesystem path for the specified sftp path
func (fs OSSFs) ResolvePath(sftpPath string) (string, error) {
	if !path.IsAbs(sftpPath) {
		sftpPath = path.Clean("/" + sftpPath)
	}
	return fs.Join("/", fs.config.KeyPrefix, sftpPath), nil
}

func (fs *OSSFs) resolve(name *string, prefix string) (string, bool) {
	result := strings.TrimPrefix(*name, prefix)
	isDir := strings.HasSuffix(result, "/")
	if isDir {
		result = strings.TrimSuffix(result, "/")
	}
	if strings.Contains(result, "/") {
		i := strings.Index(result, "/")
		isDir = true
		result = result[:i]
	}
	return result, isDir
}

func (fs *OSSFs) isEqual(ossKey *string, sftpName string) bool {
	if *ossKey == sftpName {
		return true
	}
	if "/"+*ossKey == sftpName {
		return true
	}
	if "/"+*ossKey == sftpName+"/" {
		return true
	}
	return false
}

func (fs *OSSFs) checkIfBucketExists() error {
	_, err := fs.svc.GetBucketInfo(fs.config.Bucket)
	metrics.OSSHeadBucketCompleted(err)
	return err
}

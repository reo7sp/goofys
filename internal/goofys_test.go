// Copyright 2015 Ka-Hing Cheung
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package internal

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/corehandlers"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"

	"github.com/jacobsa/fuse"
	"github.com/jacobsa/fuse/fuseops"
	"github.com/jacobsa/fuse/fuseutil"

	"github.com/Sirupsen/logrus"

	. "gopkg.in/check.v1"
)

// so I don't get complains about unused imports
var ignored = logrus.DebugLevel

func currentUid() uint32 {
	user, err := user.Current()
	if err != nil {
		panic(err)
	}

	uid, err := strconv.ParseUint(user.Uid, 10, 32)
	if err != nil {
		panic(err)
	}

	return uint32(uid)
}

func currentGid() uint32 {
	user, err := user.Current()
	if err != nil {
		panic(err)
	}

	gid, err := strconv.ParseUint(user.Gid, 10, 32)
	if err != nil {
		panic(err)
	}

	return uint32(gid)
}

type GoofysTest struct {
	fs        *Goofys
	ctx       context.Context
	awsConfig *aws.Config
	s3        *s3.S3
	sess      *session.Session
	env       map[string]io.ReadSeeker
}

func Test(t *testing.T) {
	TestingT(t)
}

var _ = Suite(&GoofysTest{})

func logOutput(t *C, tag string, r io.ReadCloser) {
	in := bufio.NewScanner(r)

	for in.Scan() {
		t.Log(tag, in.Text())
	}
}

func (s *GoofysTest) waitFor(t *C, addr string) (err error) {
	// wait for it to listen on port
	for i := 0; i < 10; i++ {
		var conn net.Conn
		conn, err = net.Dial("tcp", addr)
		if err == nil {
			// we are done!
			conn.Close()
			return
		} else {
			t.Log("Cound not connect: %v", err)
			time.Sleep(1 * time.Second)
		}
	}

	return
}

func (s *GoofysTest) SetUpSuite(t *C) {
	//addr := "play.minio.io:9000"
	const LOCAL_TEST = true

	if LOCAL_TEST {
		addr := "127.0.0.1:8080"

		err := s.waitFor(t, addr)
		t.Assert(err, IsNil)

		s.awsConfig = &aws.Config{
			//Credentials: credentials.AnonymousCredentials,
			Credentials:      credentials.NewStaticCredentials("foo", "bar", ""),
			Region:           aws.String("us-west-2"),
			Endpoint:         aws.String(addr),
			DisableSSL:       aws.Bool(true),
			S3ForcePathStyle: aws.Bool(true),
			MaxRetries:       aws.Int(0),
			//Logger: t,
			//LogLevel: aws.LogLevel(aws.LogDebug),
			//LogLevel: aws.LogLevel(aws.LogDebug | aws.LogDebugWithHTTPBody),
		}
	} else {
		s.awsConfig = &aws.Config{
			Region:     aws.String("us-west-2"),
			DisableSSL: aws.Bool(true),
			//LogLevel:         aws.LogLevel(aws.LogDebug | aws.LogDebugWithSigning),
			S3ForcePathStyle: aws.Bool(true),
		}
	}
	s.sess = session.New(s.awsConfig)
	s.s3 = s3.New(s.sess)
	s.s3.Handlers.Sign.Clear()
	s.s3.Handlers.Sign.PushBack(SignV2)
	s.s3.Handlers.Sign.PushBackNamed(corehandlers.BuildContentLengthHandler)
	_, err := s.s3.ListBuckets(nil)
	t.Assert(err, IsNil)
}

func (s *GoofysTest) TearDownSuite(t *C) {
}

func (s *GoofysTest) setupEnv(t *C, bucket string, env map[string]io.ReadSeeker) {
	_, err := s.s3.CreateBucket(&s3.CreateBucketInput{
		Bucket: &bucket,
		//ACL: aws.String(s3.BucketCannedACLPrivate),
	})
	t.Assert(err, IsNil)

	for path, r := range env {
		if r == nil {
			if strings.HasSuffix(path, "/") {
				r = bytes.NewReader([]byte{})
			} else {
				r = bytes.NewReader([]byte(path))
			}
		}

		params := &s3.PutObjectInput{
			Bucket: &bucket,
			Key:    &path,
			Body:   r,
		}

		_, err := s.s3.PutObject(params)
		t.Assert(err, IsNil)
	}

	// double check
	for path := range env {
		params := &s3.HeadObjectInput{Bucket: &bucket, Key: &path}
		_, err := s.s3.HeadObject(params)
		t.Assert(err, IsNil)
	}

	t.Log("setupEnv done")
}

// from https://stackoverflow.com/questions/22892120/how-to-generate-a-random-string-of-a-fixed-length-in-golang
func RandStringBytesMaskImprSrc(n int) string {
	const letterBytes = "abcdefghijklmnopqrstuvwxyz0123456789"
	const (
		letterIdxBits = 6                    // 6 bits to represent a letter index
		letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
		letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
	)
	src := rand.NewSource(time.Now().UnixNano())
	b := make([]byte, n)
	// A src.Int63() generates 63 random bits, enough for letterIdxMax characters!
	for i, cache, remain := n-1, src.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = src.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			b[i] = letterBytes[idx]
			i--
		}
		cache >>= letterIdxBits
		remain--
	}

	return string(b)
}

func (s *GoofysTest) setupDefaultEnv(t *C) (bucket string) {
	s.env = map[string]io.ReadSeeker{
		"file1":           nil,
		"file2":           nil,
		"dir1/file3":      nil,
		"dir2/dir3/":      nil,
		"dir2/dir3/file4": nil,
		"empty_dir/":      nil,
		"zero":            bytes.NewReader([]byte{}),
	}

	bucket = RandStringBytesMaskImprSrc(16)
	s.setupEnv(t, bucket, s.env)
	return bucket
}

func (s *GoofysTest) SetUpTest(t *C) {
	bucket := s.setupDefaultEnv(t)

	s.ctx = context.Background()

	uid, gid := MyUserAndGroup()
	flags := &FlagStorage{
		StorageClass: "STANDARD",
		DirMode:      0700,
		FileMode:     0700,
		Uid:          uint32(uid),
		Gid:          uint32(gid),
	}
	s.fs = NewGoofys(bucket, s.awsConfig, flags)
	t.Assert(s.fs, NotNil)
}

func (s *GoofysTest) getRoot(t *C) (inode *Inode) {
	inode = s.fs.inodes[fuseops.RootInodeID]
	t.Assert(inode, NotNil)
	return
}

func (s *GoofysTest) TestGetRootInode(t *C) {
	root := s.getRoot(t)
	t.Assert(root.Id, Equals, fuseops.InodeID(fuseops.RootInodeID))
}

func (s *GoofysTest) TestGetRootAttributes(t *C) {
	_, err := s.getRoot(t).GetAttributes(s.fs)
	t.Assert(err, IsNil)
}

func (s *GoofysTest) ForgetInode(t *C, inode fuseops.InodeID) {
	err := s.fs.ForgetInode(s.ctx, &fuseops.ForgetInodeOp{Inode: inode})
	t.Assert(err, IsNil)
}

func (s *GoofysTest) LookUpInode(t *C, name string) (in *Inode, err error) {
	parent := s.getRoot(t)

	for {
		idx := strings.Index(name, "/")
		if idx == -1 {
			break
		}

		dirName := name[0:idx]
		name = name[idx+1:]

		parent, err = parent.LookUp(s.fs, dirName)
		if err != nil {
			return
		}
	}

	in, err = parent.LookUp(s.fs, name)
	return
}

func (s *GoofysTest) TestLookUpInode(t *C) {
	_, err := s.LookUpInode(t, "file1")
	t.Assert(err, IsNil)

	_, err = s.LookUpInode(t, "fileNotFound")
	t.Assert(err, Equals, fuse.ENOENT)

	_, err = s.LookUpInode(t, "dir1/file3")
	t.Assert(err, IsNil)

	_, err = s.LookUpInode(t, "dir2/dir3/file4")
	t.Assert(err, IsNil)

	_, err = s.LookUpInode(t, "empty_dir")
	t.Assert(err, IsNil)
}

func (s *GoofysTest) TestGetInodeAttributes(t *C) {
	inode, err := s.getRoot(t).LookUp(s.fs, "file1")
	t.Assert(err, IsNil)

	attr, err := inode.GetAttributes(s.fs)
	t.Assert(err, IsNil)
	t.Assert(attr.Size, Equals, uint64(len("file1")))
}

func (s *GoofysTest) readDirFully(t *C, dh *DirHandle) (entries []fuseutil.Dirent) {
	en, err := dh.ReadDir(s.fs, fuseops.DirOffset(0))
	t.Assert(err, IsNil)
	t.Assert(en.Name, Equals, ".")

	en, err = dh.ReadDir(s.fs, fuseops.DirOffset(1))
	t.Assert(err, IsNil)
	t.Assert(en.Name, Equals, "..")

	for i := fuseops.DirOffset(2); ; i++ {
		en, err = dh.ReadDir(s.fs, i)
		t.Assert(err, IsNil)

		if en == nil {
			return
		}

		entries = append(entries, *en)
	}
}

func namesOf(entries []fuseutil.Dirent) (names []string) {
	for _, en := range entries {
		names = append(names, en.Name)
	}
	return
}

func (s *GoofysTest) assertEntries(t *C, in *Inode, names []string) {
	dh := in.OpenDir()
	defer dh.CloseDir()

	t.Assert(namesOf(s.readDirFully(t, dh)), DeepEquals, names)
}

func (s *GoofysTest) TestReadDir(t *C) {
	// test listing /
	dh := s.getRoot(t).OpenDir()
	defer dh.CloseDir()

	s.assertEntries(t, s.getRoot(t), []string{"dir1", "dir2", "empty_dir", "file1", "file2", "zero"})

	// test listing dir1/
	in, err := s.LookUpInode(t, "dir1")
	t.Assert(err, IsNil)
	s.assertEntries(t, in, []string{"file3"})

	// test listing dir2/
	in, err = s.LookUpInode(t, "dir2")
	t.Assert(err, IsNil)
	s.assertEntries(t, in, []string{"dir3"})

	// test listing dir2/dir3/
	in, err = in.LookUp(s.fs, "dir3")
	t.Assert(err, IsNil)
	s.assertEntries(t, in, []string{"file4"})
}

func (s *GoofysTest) TestReadFiles(t *C) {
	parent := s.getRoot(t)
	dh := parent.OpenDir()
	defer dh.CloseDir()

	for i := fuseops.DirOffset(0); ; i++ {
		en, err := dh.ReadDir(s.fs, i)
		t.Assert(err, IsNil)

		if en == nil {
			break
		}

		if en.Type == fuseutil.DT_File {
			in, err := parent.LookUp(s.fs, en.Name)
			t.Assert(err, IsNil)

			fh := in.OpenFile(s.fs)
			buf := make([]byte, 4096)

			nread, err := fh.ReadFile(s.fs, 0, buf)
			if en.Name == "zero" {
				t.Assert(nread, Equals, 0)
			} else {
				t.Assert(nread, Equals, len(en.Name))
				buf = buf[0:nread]
				t.Assert(string(buf), Equals, en.Name)
			}
		} else {

		}
	}
}

func (s *GoofysTest) TestReadOffset(t *C) {
	root := s.getRoot(t)
	f := "file1"

	in, err := root.LookUp(s.fs, f)
	t.Assert(err, IsNil)

	fh := in.OpenFile(s.fs)

	buf := make([]byte, 4096)

	nread, err := fh.ReadFile(s.fs, 1, buf)
	t.Assert(err, IsNil)
	t.Assert(nread, Equals, len(f)-1)
	t.Assert(string(buf[0:nread]), DeepEquals, f[1:])

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for i := 0; i < 3; i++ {
		off := r.Int31n(int32(len(f)))
		nread, err = fh.ReadFile(s.fs, int64(off), buf)
		t.Assert(err, IsNil)
		t.Assert(nread, Equals, len(f)-int(off))
		t.Assert(string(buf[0:nread]), DeepEquals, f[off:])
	}
}

func (s *GoofysTest) TestCreateFiles(t *C) {
	fileName := "testCreateFile"

	_, fh := s.getRoot(t).Create(s.fs, fileName)

	err := fh.FlushFile(s.fs)
	t.Assert(err, IsNil)

	resp, err := s.s3.GetObject(&s3.GetObjectInput{Bucket: &s.fs.bucket, Key: &fileName})
	t.Assert(err, IsNil)
	t.Assert(*resp.ContentLength, DeepEquals, int64(0))
	defer resp.Body.Close()

	_, err = s.getRoot(t).LookUp(s.fs, fileName)
	t.Assert(err, IsNil)

	fileName = "testCreateFile2"
	s.testWriteFile(t, fileName, 1, 128*1024)

	inode, err := s.getRoot(t).LookUp(s.fs, fileName)
	t.Assert(err, IsNil)

	fh = inode.OpenFile(s.fs)
	err = fh.FlushFile(s.fs)
	t.Assert(err, IsNil)

	resp, err = s.s3.GetObject(&s3.GetObjectInput{Bucket: &s.fs.bucket, Key: &fileName})
	t.Assert(err, IsNil)
	t.Assert(*resp.ContentLength, Equals, int64(1))
	defer resp.Body.Close()
}

func (s *GoofysTest) TestUnlink(t *C) {
	fileName := "file1"

	err := s.getRoot(t).Unlink(s.fs, fileName)
	t.Assert(err, IsNil)

	// make sure that it's gone from s3
	_, err = s.s3.GetObject(&s3.GetObjectInput{Bucket: &s.fs.bucket, Key: &fileName})
	t.Assert(mapAwsError(err), Equals, fuse.ENOENT)
}

type FileHandleReader struct {
	fs     *Goofys
	fh     *FileHandle
	offset int64
}

func (r *FileHandleReader) Read(p []byte) (nread int, err error) {
	nread, err = r.fh.ReadFile(r.fs, r.offset, p)
	r.offset += int64(nread)
	return
}

func (r *FileHandleReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		r.offset = offset
	case 1:
		r.offset += offset
	default:
		panic(fmt.Sprintf("unsupported whence: %v", whence))
	}

	return r.offset, nil
}

func (s *GoofysTest) testWriteFile(t *C, fileName string, size int64, write_size int) {
	s.testWriteFileAt(t, fileName, int64(0), size, write_size)
}

func (s *GoofysTest) testWriteFileAt(t *C, fileName string, offset int64, size int64, write_size int) {
	var fh *FileHandle

	if offset == 0 {
		_, fh = s.getRoot(t).Create(s.fs, fileName)
	} else {
		in, err := s.getRoot(t).LookUp(s.fs, fileName)
		t.Assert(err, IsNil)

		fh = in.OpenFile(s.fs)
	}

	buf := make([]byte, write_size)
	nwritten := offset

	src := io.LimitReader(&SeqReader{}, size)

	for {
		nread, err := src.Read(buf)
		if err == io.EOF {
			t.Assert(nwritten, Equals, size)
			break
		}
		t.Assert(err, IsNil)

		err = fh.WriteFile(s.fs, nwritten, buf[:nread])
		t.Assert(err, IsNil)
		nwritten += int64(nread)
	}

	err := fh.FlushFile(s.fs)
	t.Assert(err, IsNil)

	resp, err := s.s3.HeadObject(&s3.HeadObjectInput{Bucket: &s.fs.bucket, Key: &fileName})
	t.Assert(err, IsNil)
	t.Assert(*resp.ContentLength, DeepEquals, size+offset)

	fr := &FileHandleReader{s.fs, fh, offset}
	diff, err := CompareReader(fr, io.LimitReader(&SeqReader{offset}, size))
	t.Assert(err, IsNil)
	t.Assert(diff, Equals, -1)
	t.Assert(fr.offset, Equals, size)

	fh.Release()
}

func (s *GoofysTest) TestWriteLargeFile(t *C) {
	s.testWriteFile(t, "testLargeFile", 21*1024*1024, 128*1024)
	s.testWriteFile(t, "testLargeFile2", 20*1024*1024, 128*1024)
}

func (s *GoofysTest) TestReadWriteMinimumMemory(t *C) {
	s.fs.bufferPool.maxBuffersGlobal = 1
	s.testWriteFile(t, "testLargeFile", 21*1024*1024, 128*1024)
}

func (s *GoofysTest) TestWriteManyFilesFile(t *C) {
	var files sync.WaitGroup

	for i := 0; i < 21; i++ {
		files.Add(1)
		fileName := "testSmallFile" + strconv.Itoa(i)
		go func() {
			defer files.Done()
			s.testWriteFile(t, fileName, 1, 128*1024)
		}()
	}

	files.Wait()
}

func (s *GoofysTest) testWriteFileNonAlign(t *C) {
	s.testWriteFile(t, "testWriteFileNonAlign", 6*1024*1024, 128*1024+1)
}

func (s *GoofysTest) TestReadRandom(t *C) {
	size := int64(21 * 1024 * 1024)

	s.testWriteFile(t, "testLargeFile", size, 128*1024)
	in, err := s.LookUpInode(t, "testLargeFile")
	t.Assert(err, IsNil)

	fh := in.OpenFile(s.fs)
	fr := &FileHandleReader{s.fs, fh, 0}

	src := rand.NewSource(time.Now().UnixNano())
	truth := &SeqReader{}

	for i := 0; i < 10; i++ {
		offset := src.Int63() % (size / 2)

		fr.Seek(offset, 0)
		truth.Seek(offset, 0)

		// read 5MB+1 from that offset
		nread := int64(5*1024*1024 + 1)
		CompareReader(io.LimitReader(fr, nread), io.LimitReader(truth, nread))
	}
}

func (s *GoofysTest) TestMkDir(t *C) {
	_, err := s.LookUpInode(t, "new_dir/file")
	t.Assert(err, Equals, fuse.ENOENT)

	dirName := "new_dir"
	inode, err := s.getRoot(t).MkDir(s.fs, dirName)
	t.Assert(err, IsNil)
	t.Assert(*inode.FullName, Equals, dirName)

	_, err = s.LookUpInode(t, dirName)
	t.Assert(err, IsNil)

	fileName := "file"
	_, fh := inode.Create(s.fs, fileName)

	err = fh.FlushFile(s.fs)
	t.Assert(err, IsNil)

	_, err = s.LookUpInode(t, dirName+"/"+fileName)
	t.Assert(err, IsNil)
}

func (s *GoofysTest) TestRmDir(t *C) {
	root := s.getRoot(t)

	err := root.RmDir(s.fs, "dir1")
	t.Assert(err, Equals, fuse.ENOTEMPTY)

	err = root.RmDir(s.fs, "dir2")
	t.Assert(err, Equals, fuse.ENOTEMPTY)

	err = root.RmDir(s.fs, "empty_dir")
	t.Assert(err, IsNil)

}

func (s *GoofysTest) TestRename(t *C) {
	root := s.getRoot(t)

	from, to := "dir1", "new_dir"
	err := root.Rename(s.fs, from, root, to)
	t.Assert(err, Equals, fuse.ENOTEMPTY)

	dir2, err := root.LookUp(s.fs, "dir2")
	t.Assert(err, IsNil)

	from, to = "dir3", "new_dir"
	err = dir2.Rename(s.fs, from, root, to)
	t.Assert(err, Equals, fuse.ENOTEMPTY)

	from, to = "empty_dir", "dir1"
	err = root.Rename(s.fs, from, root, to)
	t.Assert(err, Equals, fuse.ENOTEMPTY)

	from, to = "empty_dir", "file1"
	err = root.Rename(s.fs, from, root, to)
	t.Assert(err, Equals, fuse.ENOTDIR)

	from, to = "file1", "empty_dir"
	err = root.Rename(s.fs, from, root, to)
	t.Assert(err, Equals, syscall.EISDIR)

	from, to = "empty_dir", "new_dir"
	err = root.Rename(s.fs, from, root, to)
	t.Assert(err, IsNil)

	from, to = "file1", "new_file"
	err = root.Rename(s.fs, from, root, to)
	t.Assert(err, IsNil)

	_, err = s.s3.HeadObject(&s3.HeadObjectInput{Bucket: &s.fs.bucket, Key: &to})
	t.Assert(err, IsNil)

	_, err = s.s3.HeadObject(&s3.HeadObjectInput{Bucket: &s.fs.bucket, Key: &from})
	t.Assert(mapAwsError(err), Equals, fuse.ENOENT)

	from, to = "file3", "new_file"
	dir, _ := s.LookUpInode(t, "dir1")
	err = dir.Rename(s.fs, from, root, to)
	t.Assert(err, IsNil)

	_, err = s.s3.HeadObject(&s3.HeadObjectInput{Bucket: &s.fs.bucket, Key: &to})
	t.Assert(err, IsNil)

	_, err = s.s3.HeadObject(&s3.HeadObjectInput{Bucket: &s.fs.bucket, Key: &from})
	t.Assert(mapAwsError(err), Equals, fuse.ENOENT)

	from, to = "no_such_file", "new_file"
	err = root.Rename(s.fs, from, root, to)
	t.Assert(err, Equals, fuse.ENOENT)

	// not really rename but can be used by rename
	from, to = s.fs.bucket+"/file2", "new_file"
	err = s.fs.copyObjectMultipart(int64(len(from)), from, to, "")
	t.Assert(err, IsNil)
}

func (s *GoofysTest) TestConcurrentRefDeref(t *C) {
	root := s.getRoot(t)

	lookupOp := fuseops.LookUpInodeOp{
		Parent: root.Id,
		Name:   "file1",
	}

	for i := 0; i < 20; i++ {
		err := s.fs.LookUpInode(nil, &lookupOp)
		t.Assert(err, IsNil)

		var wg sync.WaitGroup

		wg.Add(2)
		go func() {
			// we want to yield to the forget goroutine so that it's run first
			// to trigger this bug
			if i%2 == 0 {
				runtime.Gosched()
			}
			s.fs.LookUpInode(nil, &lookupOp)
			wg.Done()
		}()
		go func() {
			s.fs.ForgetInode(nil, &fuseops.ForgetInodeOp{
				Inode: lookupOp.Entry.Child,
				N:     1,
			})
			wg.Done()
		}()

		wg.Wait()
	}
}

func isTravis() bool {
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TRAVIS=") {
			return true
		}
	}

	return false
}

func (s *GoofysTest) runFuseTest(t *C, mountPoint string, umount bool, cmdArgs ...string) {
	server := fuseutil.NewFileSystemServer(s.fs)

	// Mount the file system.
	mountCfg := &fuse.MountConfig{
		FSName:                  s.fs.bucket,
		Options:                 s.fs.flags.MountOptions,
		ErrorLogger:             GetStdLogger(NewLogger("fuse"), logrus.ErrorLevel),
		DisableWritebackCaching: true,
	}

	_, err := fuse.Mount(mountPoint, server, mountCfg)
	t.Assert(err, IsNil)

	if umount {
		defer func() {
			err := fuse.Unmount(mountPoint)
			t.Assert(err, IsNil)
		}()
	}

	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Env = append(cmd.Env, os.Environ()...)
	cmd.Env = append(cmd.Env, "TRAVIS=true")
	cmd.Env = append(cmd.Env, "FAST=true")

	if isTravis() {
		logger := NewLogger("test")
		lvl := logrus.InfoLevel
		logger.Formatter.(*logHandle).lvl = &lvl
		w := logger.Writer()

		cmd.Stdout = w
		cmd.Stderr = w
	}

	err = cmd.Run()
	t.Assert(err, IsNil)
}

func (s *GoofysTest) TestFuse(t *C) {
	mountPoint := "/tmp/mnt" + s.fs.bucket

	err := os.MkdirAll(mountPoint, 0700)
	t.Assert(err, IsNil)

	defer os.Remove(mountPoint)

	s.runFuseTest(t, mountPoint, true, "../test/fuse-test.sh", mountPoint)
}

func (s *GoofysTest) TestBenchLs(t *C) {
	mountPoint := "/tmp/mnt" + s.fs.bucket

	err := os.MkdirAll(mountPoint, 0700)
	t.Assert(err, IsNil)

	defer os.Remove(mountPoint)

	s.runFuseTest(t, mountPoint, false, "../bench/bench.sh", "cat", mountPoint, "ls")
}

func (s *GoofysTest) TestBenchCreate(t *C) {
	mountPoint := "/tmp/mnt" + s.fs.bucket

	err := os.MkdirAll(mountPoint, 0700)
	t.Assert(err, IsNil)

	defer os.Remove(mountPoint)

	s.runFuseTest(t, mountPoint, false, "../bench/bench.sh", "cat", mountPoint, "create")
}

func (s *GoofysTest) TestBenchCreateParallel(t *C) {
	mountPoint := "/tmp/mnt" + s.fs.bucket

	err := os.MkdirAll(mountPoint, 0700)
	t.Assert(err, IsNil)

	defer os.Remove(mountPoint)

	s.runFuseTest(t, mountPoint, false, "../bench/bench.sh", "cat", mountPoint, "create_parallel")
}

func (s *GoofysTest) TestBenchIO(t *C) {
	mountPoint := "/tmp/mnt" + s.fs.bucket

	err := os.MkdirAll(mountPoint, 0700)
	t.Assert(err, IsNil)

	defer os.Remove(mountPoint)

	s.runFuseTest(t, mountPoint, false, "../bench/bench.sh", "cat", mountPoint, "md5")
}

func (s *GoofysTest) TestChmod(t *C) {
	root := s.getRoot(t)

	lookupOp := fuseops.LookUpInodeOp{
		Parent: root.Id,
		Name:   "file1",
	}

	err := s.fs.LookUpInode(nil, &lookupOp)
	t.Assert(err, IsNil)

	targetMode := os.FileMode(0777)
	setOp := fuseops.SetInodeAttributesOp{Inode: lookupOp.Entry.Child, Mode: &targetMode}

	err = s.fs.SetInodeAttributes(s.ctx, &setOp)
	t.Assert(err, IsNil)
	t.Assert(setOp.Attributes, NotNil)
}

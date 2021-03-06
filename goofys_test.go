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

package main

import (
	"bufio"
	"bytes"
	"net"
	"io"
	"math/rand"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/service/s3"

	"github.com/jacobsa/fuse"
	"github.com/jacobsa/fuse/fuseops"
	"github.com/jacobsa/fuse/fuseutil"

	"github.com/minio/minio/pkg/auth"
	"github.com/minio/minio/pkg/server"
	"github.com/minio/minio/pkg/server/api"

	. "gopkg.in/check.v1"
)

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
	fs *Goofys
	ctx context.Context
	awsConfig *aws.Config
	s3 *s3.S3
	env map[string]io.ReadSeeker
}

type S3Proxy struct {
	jar string
	config string
	cmd *exec.Cmd
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

func (s *GoofysTest) setupMinio(t *C, addr string) (accessKey string, secretKey string) {
	accessKeyID, perr := auth.GenerateAccessKeyID()
	t.Assert(perr, IsNil)
	secretAccessKey, perr := auth.GenerateSecretAccessKey()
	t.Assert(perr, IsNil)

	accessKey = string(accessKeyID)
	secretKey = string(secretAccessKey)

	authConf := &auth.Config{}
	authConf.Users = make(map[string]*auth.User)
	authConf.Users[string(accessKeyID)] = &auth.User{
		Name:            "testuser",
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
	}
	auth.SetAuthConfigPath(filepath.Join(t.MkDir(), "users.json"))
	perr = auth.SaveConfig(authConf)
	t.Assert(perr, IsNil)

	go server.Start(api.Config{ Address: addr })

	err := s.waitFor(t, addr)
	t.Assert(err, IsNil)

	return
}

func (s *GoofysTest) SetUpSuite(t *C) {
	//addr := "play.minio.io:9000"
	addr := "127.0.0.1:9000"

	accessKey, secretKey := s.setupMinio(t, addr)

	s.awsConfig = &aws.Config{
		//Credentials: credentials.AnonymousCredentials,
		Credentials: credentials.NewStaticCredentials(accessKey, secretKey, ""),
		Region: aws.String("milkyway"),//aws.String("us-west-2"),
		Endpoint: aws.String(addr),
		DisableSSL: aws.Bool(true),
		S3ForcePathStyle: aws.Bool(true),
		MaxRetries: aws.Int(0),
		Logger: t,
		LogLevel: aws.LogLevel(aws.LogDebug),
		//LogLevel: aws.LogLevel(aws.LogDebug | aws.LogDebugWithHTTPBody),
	}
	s.s3 = s3.New(s.awsConfig)

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
			r = bytes.NewReader([]byte(path))
		}

		params := &s3.PutObjectInput{
			Bucket: &bucket,
			Key: &path,
			Body: r,
		}


		_, err := s.s3.PutObject(params)
		t.Assert(err, IsNil)
	}

	// double check
	for path := range env {
		params := &s3.HeadObjectInput{ Bucket: &bucket, Key: &path }
		_, err := s.s3.HeadObject(params)
		t.Assert(err, IsNil)
	}

	t.Log("setupEnv done")
}


// from https://stackoverflow.com/questions/22892120/how-to-generate-a-random-string-of-a-fixed-length-in-golang
func RandStringBytesMaskImprSrc(n int) string {
	const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
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
		"file1": nil,
		"file2": nil,
		"dir1/file3": nil,
		"dir2/dir3/file4": nil,
		"empty_dir/": nil,
	}

	bucket = RandStringBytesMaskImprSrc(16)
	s.setupEnv(t, bucket, s.env)
	return bucket
}

func (s *GoofysTest) SetUpTest(t *C) {
	bucket := s.setupDefaultEnv(t)

	s.fs = NewGoofys(bucket, s.awsConfig, currentUid(), currentGid())
	s.ctx = context.Background()
}

func (s *GoofysTest) getRoot(t *C) *Inode {
	return s.fs.inodes[fuseops.RootInodeID]
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
	err := s.fs.ForgetInode(s.ctx, &fuseops.ForgetInodeOp{ Inode: inode })
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
		name = name[idx + 1:]

		parent, err = parent.LookUp(s.fs, &dirName)
		if err != nil {
			return
		}
	}

	in, err = parent.LookUp(s.fs, &name)
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
	inode, err := s.getRoot(t).LookUp(s.fs, aws.String("file1"))
	t.Assert(err, IsNil)

	attr, err := inode.GetAttributes(s.fs)
	t.Assert(err, IsNil)
	t.Assert(attr.Size, Equals, uint64(len("file1")))
}

func (s *GoofysTest) readDirFully(t *C, dh *DirHandle) (entries []fuseutil.Dirent) {
	for i := fuseops.DirOffset(0); ; i++ {
		en, err := dh.ReadDir(s.fs, i)
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

	s.assertEntries(t, s.getRoot(t), []string{ "dir1", "dir2", "empty_dir", "file1", "file2" })

	// test listing dir1/
	in, err := s.LookUpInode(t, "dir1")
	t.Assert(err, IsNil)
	s.assertEntries(t, in, []string{ "file3" })

	// test listing dir2/
	in, err = s.LookUpInode(t, "dir2")
	t.Assert(err, IsNil)
	s.assertEntries(t, in, []string{ "dir3" })

	// test listing dir2/dir3/
	in, err = in.LookUp(s.fs, aws.String("dir3"))
	t.Assert(err, IsNil)
	s.assertEntries(t, in, []string{ "file4" })
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
			in, err := parent.LookUp(s.fs, &en.Name)
			t.Assert(err, IsNil)

			fh := in.OpenFile(s.fs)
			buf := make([]byte, 4096)

			nread, err := fh.ReadFile(s.fs, 0, buf)
			t.Assert(nread, Equals, len(en.Name))
			buf = buf[0 : nread]
			t.Assert(string(buf), Equals, en.Name)
		} else {

		}
	}
}

func (s *GoofysTest) TestCreateFiles(t *C) {
	fileName := "testCreateFile"

	_, fh := s.getRoot(t).Create(s.fs, &fileName)

	err := fh.FlushFile(s.fs)
	t.Assert(err, IsNil)

	resp, err := s.s3.GetObject(&s3.GetObjectInput{ Bucket: &s.fs.bucket, Key: &fileName })
	defer resp.Body.Close()

	t.Assert(err, IsNil)
	t.Assert(*resp.ContentLength, DeepEquals, int64(0))

	_, err = s.getRoot(t).LookUp(s.fs, &fileName)
	t.Assert(err, IsNil)
}

func (s *GoofysTest) TestUnlink(t *C) {
	t.Skip("minio doesn't support unlink")
	fileName := "file1"

	err := s.getRoot(t).Unlink(s.fs, &fileName)
	t.Assert(err, IsNil)

	// make sure that it's gone from s3
	_, err = s.s3.GetObject(&s3.GetObjectInput{ Bucket: &s.fs.bucket, Key: &fileName })
	t.Assert(mapAwsError(err), Equals, fuse.ENOENT)

	err = s.getRoot(t).Unlink(s.fs, &fileName)
	t.Assert(err, Equals, fuse.ENOENT)
}

func (s *GoofysTest) TestWriteLargeFile(t *C) {
	fileName := "testLargeFile"

	_, fh := s.getRoot(t).Create(s.fs, &fileName)

	const size = 11 * 1024 * 1024
	const write_size = 128 * 1024
	const num_writes = size / write_size

	buf := [write_size]byte{}

	for i := 0; i < num_writes; i++ {
		err := fh.WriteFile(s.fs, int64(i * write_size), buf[:])
		t.Assert(err, IsNil)
	}

	err := fh.FlushFile(s.fs)
	t.Assert(err, IsNil)

	resp, err := s.s3.HeadObject(&s3.HeadObjectInput{ Bucket: &s.fs.bucket, Key: &fileName })
	t.Assert(err, IsNil)
	t.Assert(*resp.ContentLength, DeepEquals, int64(size))
}

//go:build oss

package simplex

import (
	"testing"
)

// --- Unit Tests ---

func TestResolveOSSAddr_FileMode(t *testing.T) {
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?ak=test-ak&sk=test-sk&mode=file")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config := addr.Config().(*OSSConfig)
	if config.Mode != "file" {
		t.Fatalf("expected mode=file, got %q", config.Mode)
	}
	if config.AccessKeyId != "test-ak" {
		t.Fatalf("expected ak=test-ak, got %q", config.AccessKeyId)
	}
}

func TestResolveOSSAddr_MirrorMode(t *testing.T) {
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?ak=test-ak&sk=test-sk&mode=mirror&server=:9090")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config := addr.Config().(*OSSConfig)
	if config.Mode != "mirror" {
		t.Fatalf("expected mode=mirror, got %q", config.Mode)
	}
	if config.ServerAddr != ":9090" {
		t.Fatalf("expected server=:9090, got %q", config.ServerAddr)
	}
}

func TestResolveOSSAddr_AutoDetectMode(t *testing.T) {
	// With server= parameter, should auto-detect mirror
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?ak=test&sk=test&server=:8080")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config := addr.Config().(*OSSConfig)
	if config.Mode != "mirror" {
		t.Fatalf("expected auto-detect mode=mirror when server= present, got %q", config.Mode)
	}

	// Without server= parameter, should default to file
	addr2, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/?ak=test&sk=test")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config2 := addr2.Config().(*OSSConfig)
	if config2.Mode != "file" {
		t.Fatalf("expected default mode=file without server=, got %q", config2.Mode)
	}
}

func TestResolveOSSAddr_Constants(t *testing.T) {
	addr, err := ResolveOSSAddr("oss", "oss://bucket.oss-cn-shanghai.aliyuncs.com/prefix/")
	if err != nil {
		t.Fatalf("ResolveOSSAddr: %v", err)
	}
	config := addr.Config().(*OSSConfig)

	// Verify updated constants
	if config.Interval.Milliseconds() != 2000 {
		t.Fatalf("expected default interval=2000ms, got %dms", config.Interval.Milliseconds())
	}
	if config.MaxBodySize != 1024*1024 {
		t.Fatalf("expected MaxBodySize=1MB, got %d", config.MaxBodySize)
	}
}

func TestMockListObjects_PrefixFilter(t *testing.T) {
	mock := newMockOSSConnector()

	// Put objects with different prefixes
	mock.PutObject("test/client1_send", []byte("data1"))
	mock.PutObject("test/client2_send", []byte("data2"))
	mock.PutObject("test/client1_recv", []byte("data3"))
	mock.PutObject("other/client3_send", []byte("data4"))

	// List with prefix "test/"
	objects, err := mock.ListObjects("test/")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}

	// Should find 3 objects (test/ prefix only)
	if len(objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(objects))
	}

	// List with prefix "other/"
	objects2, err := mock.ListObjects("other/")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(objects2) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objects2))
	}
}

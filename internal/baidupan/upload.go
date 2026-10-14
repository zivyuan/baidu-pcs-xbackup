package baidupan

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const chunkSize = 4 << 20 // 4MB

// md5Chunks 计算文件的分片 md5(4MB 一片),oauth 与 web 上传共用。
// 返回文件大小、分片 md5 列表及其 JSON 序列化(precreate/create 的 block_list)。
func md5Chunks(localPath string) (size int64, md5s []string, blockJSON string, err error) {
	f, err := os.Open(localPath)
	if err != nil {
		return 0, nil, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, nil, "", err
	}
	size = st.Size()
	buf := make([]byte, chunkSize)
	for {
		n, rerr := io.ReadFull(f, buf)
		if n > 0 {
			h := md5.Sum(buf[:n])
			md5s = append(md5s, hex.EncodeToString(h[:]))
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return 0, nil, "", rerr
		}
	}
	if size == 0 {
		md5s = []string{} // 空文件 block_list 为空数组
	}
	j, _ := json.Marshal(md5s)
	return size, md5s, string(j), nil
}

// UploadFile 官方三步上传:precreate → superfile2 分片 → create。
// 秒传命中直接返回;分片失败重试 3 次(1s/2s/4s)。
func (c *Client) UploadFile(remotePath, localPath string) error {
	if err := c.EnsureToken(); err != nil {
		return err
	}
	size, md5s, blockJSON, err := md5Chunks(localPath)
	if err != nil {
		return err
	}

	// 2. precreate
	preForm := url.Values{
		"path":       {remotePath},
		"isdir":      {"0"},
		"size":       {fmt.Sprint(size)},
		"autoinit":   {"1"},
		"rtype":      {"3"}, // 同名覆盖
		"block_list": {string(blockJSON)},
	}
	var pre struct {
		Errno      int64  `json:"errno"`
		ErrorMsg   string `json:"error_msg"`
		UploadID   string `json:"uploadid"`
		ReturnType int    `json:"return_type"`
		BlockList  []int  `json:"block_list"`
	}
	if err := c.callPOST(&pre, "/rest/2.0/xpan/file",
		url.Values{"method": {"precreate"}}, preForm); err != nil {
		return fmt.Errorf("预上传 %s: %w", remotePath, err)
	}
	// 注意:实测部分应用 precreate 恒返回 return_type=1 但文件并未创建,
	// 不可据此判定秒传成功。仅当"无 uploadid 且无需上传分片"才视为真秒传。
	need := pre.BlockList // 需要实际上传的分片序号
	if len(need) == 0 && size > 0 && pre.UploadID == "" {
		return nil // 真秒传命中
	}
	if len(need) == 0 && size > 0 {
		for i := range md5s {
			need = append(need, i)
		}
	}

	// 3. superfile2 逐分片上传(带重试)
	for _, seq := range need {
		if err := c.uploadChunk(pre.UploadID, remotePath, seq, localPath); err != nil {
			return fmt.Errorf("分片 %d 上传 %s: %w", seq, remotePath, err)
		}
	}

	// 4. create
	createForm := url.Values{
		"path":       {remotePath},
		"isdir":      {"0"},
		"size":       {fmt.Sprint(size)},
		"uploadid":   {pre.UploadID},
		"rtype":      {"3"},
		"block_list": {string(blockJSON)},
	}
	var cr struct {
		Errno      int64  `json:"errno"`
		ErrorMsg   string `json:"error_msg"`
		ReturnType int    `json:"return_type"`
	}
	if err := c.callPOST(&cr, "/rest/2.0/xpan/file",
		url.Values{"method": {"create"}}, createForm); err != nil {
		return fmt.Errorf("创建文件 %s: %w", remotePath, err)
	}
	return nil
}

// uploadChunk 上传单个分片到 d.pcs.baidu.com,失败重试 3 次(1s/2s/4s)。
func (c *Client) uploadChunk(uploadID, remotePath string, seq int, localPath string) error {
	offset := int64(seq) * chunkSize
	chunk := make([]byte, chunkSize)
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<(attempt-1)) * time.Second)
		}
		f, err := os.Open(localPath)
		if err != nil {
			return err
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		n, rerr := io.ReadFull(f, chunk)
		f.Close()
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return rerr
		}

		params := url.Values{
			"access_token": {c.token.AccessToken},
			"method":       {"upload"},
			"type":         {"tmpfile"},
			"path":         {remotePath},
			"uploadid":     {uploadID},
			"partseq":      {fmt.Sprint(seq)},
		}
		req, err := http.NewRequest("PUT",
			"https://d.pcs.baidu.com/rest/2.0/pcs/superfile2?"+params.Encode(),
			bytes.NewReader(chunk[:n]))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = c.redact(err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		var out struct {
			Errno int64  `json:"errno"`
			Error string `json:"error_msg"`
		}
		json.Unmarshal(body, &out)
		if resp.StatusCode == 200 && out.Errno == 0 {
			return nil
		}
		lastErr = fmt.Errorf("superfile2 errno=%d http=%d", out.Errno, resp.StatusCode)
	}
	return lastErr
}

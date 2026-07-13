package admin

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/importjobs"
)

// ListImportJobs GET /admin/import/jobs
func (h *Handlers) ListImportJobs(w http.ResponseWriter, r *http.Request) {
	if h.ImportJobs == nil {
		writeErr(w, http.StatusServiceUnavailable, "导入任务未启用")
		return
	}
	list := h.ImportJobs.List(50)
	if list == nil {
		list = []importjobs.Job{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": list,
		"limits": map[string]any{
			"max_upload_bytes":         h.Config.Imports.MaxUploadBytes,
			"max_entries":              h.Config.Imports.MaxEntries,
			"sso_converter_configured": strings.TrimSpace(h.Config.Imports.SSOConverter.Endpoint) != "" && strings.TrimSpace(h.Config.Imports.SSOConverter.APIKey) != "",
		},
	})
}

// CreateImportJob POST /admin/import/jobs。
// 浏览器使用 multipart(format,file)；旧 JSON path 协议仅在配置显式开启时接受。
func (h *Handlers) CreateImportJob(w http.ResponseWriter, r *http.Request) {
	if h.ImportJobs == nil || !h.Config.Imports.Enabled {
		writeErr(w, http.StatusServiceUnavailable, "导入任务未启用")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type 必须是 multipart/form-data")
		return
	}
	switch mediaType {
	case "multipart/form-data":
		h.createUploadImportJob(w, r)
	case "application/json":
		if !h.Config.Imports.AllowServerPath {
			writeErr(w, http.StatusUnsupportedMediaType, "服务端路径导入未启用")
			return
		}
		var req importjobs.CreateRequest
		if err := decodeJSON(r, 1<<20, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "导入请求无效")
			return
		}
		job, err := h.ImportJobs.Submit(req)
		if err != nil {
			writeImportJobError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	default:
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type 必须是 multipart/form-data")
	}
}

func (h *Handlers) createUploadImportJob(w http.ResponseWriter, r *http.Request) {
	permit, err := h.ImportJobs.ReserveUpload()
	if err != nil {
		writeImportJobError(w, err)
		return
	}
	defer permit.Release()
	maxRequest := h.Config.Imports.MaxRequestBytes
	if maxRequest <= 0 {
		maxRequest = config.DefaultImportMaxRequestBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	reader, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "multipart 请求无效")
		return
	}
	var (
		format     string
		formatSeen bool
		fileSeen   bool
		upload     importjobs.StagedUpload
		partCount  int
	)
	defer func() { _ = upload.Remove() }()
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeErr(w, http.StatusRequestEntityTooLarge, "上传请求超过大小限制")
			} else {
				writeErr(w, http.StatusBadRequest, "multipart 请求无效")
			}
			return
		}
		partCount++
		if partCount > 3 {
			_ = part.Close()
			writeErr(w, http.StatusBadRequest, "multipart 字段过多")
			return
		}
		switch part.FormName() {
		case "format":
			if formatSeen || part.FileName() != "" {
				_ = part.Close()
				writeErr(w, http.StatusBadRequest, "format 字段重复或无效")
				return
			}
			formatSeen = true
			raw, readErr := io.ReadAll(io.LimitReader(part, 1025))
			_ = part.Close()
			if readErr != nil {
				var maxErr *http.MaxBytesError
				if errors.As(readErr, &maxErr) {
					writeErr(w, http.StatusRequestEntityTooLarge, "上传请求超过大小限制")
				} else {
					writeErr(w, http.StatusBadRequest, "format 字段无效")
				}
				return
			}
			if len(raw) > 1024 {
				writeErr(w, http.StatusBadRequest, "format 字段无效")
				return
			}
			format = strings.TrimSpace(string(raw))
		case "file":
			if fileSeen || strings.TrimSpace(part.FileName()) == "" {
				_ = part.Close()
				writeErr(w, http.StatusBadRequest, "file 字段重复或无效")
				return
			}
			fileSeen = true
			upload, err = h.ImportJobs.StageReservedUpload(part, part.FileName(), h.Config.Imports.MaxUploadBytes, permit)
			_ = part.Close()
			if err != nil {
				var maxErr *http.MaxBytesError
				if errors.Is(err, importjobs.ErrUploadTooLarge) || errors.As(err, &maxErr) {
					writeErr(w, http.StatusRequestEntityTooLarge, "文件或上传请求超过大小限制")
				} else if errors.Is(err, importjobs.ErrEmptyUpload) {
					writeErr(w, http.StatusBadRequest, "上传文件为空")
				} else {
					writeErr(w, http.StatusInternalServerError, "无法暂存上传文件")
				}
				return
			}
		default:
			_ = part.Close()
			writeErr(w, http.StatusBadRequest, "multipart 包含未知字段")
			return
		}
	}
	if !formatSeen || format == "" {
		writeErr(w, http.StatusBadRequest, "缺少 format 字段")
		return
	}
	if !fileSeen || upload.Path == "" {
		writeErr(w, http.StatusBadRequest, "缺少 file 字段")
		return
	}
	job, err := h.ImportJobs.SubmitReservedUpload(format, upload, permit)
	if err != nil {
		writeImportJobError(w, err)
		return
	}
	upload = importjobs.StagedUpload{}
	writeJSON(w, http.StatusAccepted, job)
}

func writeImportJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, importjobs.ErrBusy):
		writeErr(w, http.StatusTooManyRequests, "导入任务已满，请稍后重试")
	case errors.Is(err, importjobs.ErrConverterUnavailable):
		writeErr(w, http.StatusServiceUnavailable, "SSO 转换器未配置")
	case errors.Is(err, importjobs.ErrClosed):
		writeErr(w, http.StatusServiceUnavailable, "导入任务正在关闭")
	case errors.Is(err, importjobs.ErrInvalidPath),
		errors.Is(err, importjobs.ErrInvalidFormat),
		errors.Is(err, importjobs.ErrServerPathDisabled):
		writeErr(w, http.StatusBadRequest, "导入请求无效")
	default:
		writeErr(w, http.StatusInternalServerError, "无法创建导入任务")
	}
}

// GetImportJob GET /admin/import/jobs/{id}
func (h *Handlers) GetImportJob(w http.ResponseWriter, r *http.Request) {
	if h.ImportJobs == nil {
		writeErr(w, http.StatusServiceUnavailable, "导入任务未启用")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 job id")
		return
	}
	job, err := h.ImportJobs.Get(id)
	if err != nil {
		if errors.Is(err, importjobs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "任务不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

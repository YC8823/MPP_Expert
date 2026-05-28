package chatpipeline

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/infrastructure/chunker"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// PluginVisionAugment injects knowledge-base images into chatManage.Images so
// that the downstream multimodal LLM receives visual context alongside the
// retrieved text chunks.
//
// For each merged chunk that contains Markdown image references
// (![...](figures/xxx.png)), the plugin:
//  1. Extracts relative image paths via chunker.ExtractImageRefs.
//  2. Resolves the owning knowledge entry's file_path (local://tenant/id/ts.md).
//  3. Constructs a local:// image URL by replacing the filename with the
//     relative image path (e.g. local://10000/<id>/figures/xxx.png).
//
// The constructed URLs are handled by resolveImageURLForLLM, which reads them
// from disk and converts them to base64 data URIs before sending to the LLM.
//
// The plugin is a no-op when:
//   - The selected chat model does not support vision.
//   - No merged chunks contain image references.
type PluginVisionAugment struct {
	knowledgeService interfaces.KnowledgeService
}

// NewPluginVisionAugment creates and registers PluginVisionAugment.
func NewPluginVisionAugment(eventManager *EventManager, knowledgeService interfaces.KnowledgeService) *PluginVisionAugment {
	p := &PluginVisionAugment{knowledgeService: knowledgeService}
	eventManager.Register(p)
	return p
}

// ActivationEvents returns the event type this plugin handles.
func (p *PluginVisionAugment) ActivationEvents() []types.EventType {
	return []types.EventType{types.VISION_AUGMENT}
}

// OnEvent augments chatManage.Images with images referenced by the merged chunks.
func (p *PluginVisionAugment) OnEvent(
	ctx context.Context,
	_ types.EventType,
	chatManage *types.ChatManage,
	next func() *PluginError,
) *PluginError {
	if !chatManage.ChatModelSupportsVision || len(chatManage.MergeResult) == 0 {
		return next()
	}

	// Group relative image refs by knowledge ID.
	// Only relative paths are handled here; http/data URIs are already supported
	// by the existing user-image pipeline and need no special treatment.
	knowledgeRefs := make(map[string][]string) // knowledgeID → []relPath
	for _, result := range chatManage.MergeResult {
		refs := chunker.ExtractImageRefs(result.Content)
		for _, ref := range refs {
			url := ref.OriginalRef
			if strings.HasPrefix(url, "http://") ||
				strings.HasPrefix(url, "https://") ||
				strings.HasPrefix(url, "data:") ||
				strings.HasPrefix(url, "local://") {
				continue
			}
			knowledgeRefs[result.KnowledgeID] = append(knowledgeRefs[result.KnowledgeID], url)
		}
	}

	if len(knowledgeRefs) == 0 {
		return next()
	}

	// Batch-fetch the file_path for each knowledge entry.
	knowledgeIDs := make([]string, 0, len(knowledgeRefs))
	for id := range knowledgeRefs {
		knowledgeIDs = append(knowledgeIDs, id)
	}

	tenantID, _ := types.TenantIDFromContext(ctx)
	if tenantID == 0 {
		tenantID = chatManage.TenantID
	}

	knowledgeList, err := p.knowledgeService.GetKnowledgeBatch(ctx, tenantID, knowledgeIDs)
	if err != nil {
		pipelineWarn(ctx, "VisionAugment", "fetch_knowledge_failed", map[string]interface{}{
			"error": err.Error(),
		})
		return next()
	}

	// Build knowledgeID → directory prefix map.
	// e.g. "local://10000/75db1990-.../1779936643147786670.md"
	//   →  "local://10000/75db1990-.../"
	dirPrefix := make(map[string]string, len(knowledgeList))
	for _, k := range knowledgeList {
		if k == nil || k.FilePath == "" {
			continue
		}
		idx := strings.LastIndex(k.FilePath, "/")
		if idx < 0 {
			continue
		}
		dirPrefix[k.ID] = k.FilePath[:idx+1]
	}

	// Append unique local:// image URLs to chatManage.Images.
	seen := make(map[string]bool, len(chatManage.Images))
	for _, img := range chatManage.Images {
		seen[img] = true
	}

	added := 0
	for knowledgeID, relPaths := range knowledgeRefs {
		prefix, ok := dirPrefix[knowledgeID]
		if !ok {
			continue
		}
		for _, rel := range relPaths {
			localURL := prefix + rel
			if !seen[localURL] {
				seen[localURL] = true
				chatManage.Images = append(chatManage.Images, localURL)
				added++
			}
		}
	}

	pipelineInfo(ctx, "VisionAugment", "images_augmented", map[string]interface{}{
		"added":       added,
		"total_images": len(chatManage.Images),
	})

	return next()
}

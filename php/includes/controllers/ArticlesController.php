<?php
/**
 * Articles Controller
 */

require_once __DIR__ . '/BaseController.php';

class ArticlesController extends BaseController {

    /**
     * Show article by number
     */
    public function show($params) {
        $groupName = $params['group'] ?? '';
        $articleNum = (int)($params['articleNum'] ?? 0);

        if (!$groupName || !$articleNum) {
            $this->renderError('Invalid article', 'Group name and article number are required', 400);
        }

        try {
            $article = $this->api->getArticle($groupName, $articleNum);

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Groups', 'url' => '/groups'],
                ['title' => $groupName, 'url' => "/groups/{$groupName}"],
                ['title' => "Article #{$articleNum}", 'url' => "/groups/{$groupName}/articles/{$articleNum}"]
            ]);

            $this->render('article', [
                'title' => $article['subject'] ?? "Article #{$articleNum}",
                'article' => $article,
                'group_name' => $groupName,
                'breadcrumb' => $breadcrumb
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load article', $e->getMessage());
        }
    }

    /**
     * Show article by message ID
     */
    public function showByMessageId($params) {
        $messageId = $params['messageId'] ?? '';

        if (!$messageId) {
            $this->renderError('Invalid message ID', 'Message ID is required', 400);
        }

        // Decode URL-encoded message ID
        $messageId = urldecode($messageId);

        try {
            $article = $this->api->getArticleByMessageId($messageId);

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Articles', 'url' => '/articles'],
                ['title' => 'Message ID: ' . truncate($messageId, 50), 'url' => "/articles/" . urlencode($messageId)]
            ]);

            $this->render('article', [
                'title' => $article['subject'] ?? 'Article',
                'article' => $article,
                'message_id' => $messageId,
                'breadcrumb' => $breadcrumb
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load article', $e->getMessage());
        }
    }
}

<?php
/**
 * Sections Controller
 */

require_once __DIR__ . '/BaseController.php';

class SectionsController extends BaseController {

    /**
     * Show section page - groups in section
     */
    public function show($params) {
        $sectionName = $params['section'] ?? '';
        $page = (int)($_GET['page'] ?? 1);
        $pageSize = min(50, max(1, (int)($_GET['page_size'] ?? 20)));

        if (!$sectionName) {
            $this->renderError('Invalid section', 'Section name is required', 400);
        }

        try {
            $result = $this->api->getSectionGroups($sectionName, $page, $pageSize);

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Sections', 'url' => '/'],
                ['title' => $sectionName, 'url' => "/{$sectionName}"]
            ]);

            $this->render('section', [
                'title' => $sectionName,
                'section_name' => $sectionName,
                'groups' => $result['groups'] ?? [],
                'pagination' => $result['pagination'] ?? null,
                'breadcrumb' => $breadcrumb,
                'current_page' => $page,
                'page_size' => $pageSize
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load section', $e->getMessage());
        }
    }

    /**
     * Show group within section
     */
    public function showGroup($params) {
        $sectionName = $params['section'] ?? '';
        $groupName = $params['group'] ?? '';
        $page = (int)($_GET['page'] ?? 1);
        $pageSize = min(50, max(1, (int)($_GET['page_size'] ?? 20)));

        if (!$sectionName || !$groupName) {
            $this->renderError('Invalid parameters', 'Section and group names are required', 400);
        }

        try {
            $result = $this->api->getGroupOverview($groupName, $page, $pageSize);

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Sections', 'url' => '/'],
                ['title' => $sectionName, 'url' => "/{$sectionName}"],
                ['title' => $groupName, 'url' => "/{$sectionName}/{$groupName}"]
            ]);

            $this->render('section_group', [
                'title' => $groupName,
                'section_name' => $sectionName,
                'group_name' => $groupName,
                'articles' => $result['articles'] ?? [],
                'pagination' => $result['pagination'] ?? null,
                'breadcrumb' => $breadcrumb,
                'current_page' => $page,
                'page_size' => $pageSize
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load group', $e->getMessage());
        }
    }

    /**
     * Show article within section/group
     */
    public function showArticle($params) {
        $sectionName = $params['section'] ?? '';
        $groupName = $params['group'] ?? '';
        $articleNum = (int)($params['articleNum'] ?? 0);

        if (!$sectionName || !$groupName || !$articleNum) {
            $this->renderError('Invalid parameters', 'Section, group, and article number are required', 400);
        }

        try {
            $article = $this->api->getArticle($groupName, $articleNum);

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Sections', 'url' => '/'],
                ['title' => $sectionName, 'url' => "/{$sectionName}"],
                ['title' => $groupName, 'url' => "/{$sectionName}/{$groupName}"],
                ['title' => "Article #{$articleNum}", 'url' => "/{$sectionName}/{$groupName}/articles/{$articleNum}"]
            ]);

            $this->render('section_article', [
                'title' => $article['subject'] ?? "Article #{$articleNum}",
                'section_name' => $sectionName,
                'group_name' => $groupName,
                'article' => $article,
                'breadcrumb' => $breadcrumb
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load article', $e->getMessage());
        }
    }
}

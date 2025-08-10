<?php
/**
 * Groups Controller
 */

require_once __DIR__ . '/BaseController.php';

class GroupsController extends BaseController {

    /**
     * Groups listing page
     */
    public function index($params) {
        list($page, $pageSize) = $this->getPaginationParams();

        try {
            $result = $this->api->getNewsgroups($page, $pageSize);

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Groups', 'url' => '/groups']
            ]);

            // Go backend returns data in 'data' field, not 'newsgroups'
            $groups = $result['data'] ?? [];
            $pagination = [
                'current_page' => $result['page'] ?? $page,
                'page_size' => $result['page_size'] ?? $pageSize,
                'total_count' => $result['total_count'] ?? 0,
                'total_pages' => $result['total_pages'] ?? 1,
                'has_next' => $result['has_next'] ?? false,
                'has_prev' => $result['has_prev'] ?? false
            ];

            $this->render('groups', [
                'title' => 'Newsgroups',
                'groups' => $groups,
                'pagination' => $pagination,
                'breadcrumb' => $breadcrumb,
                'current_page' => $page,
                'page_size' => $pageSize
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load groups', $e->getMessage());
        }
    }

    /**
     * Single group page - articles overview
     */
    public function show($params) {
        $groupName = $params['group'] ?? '';
        list($page, $pageSize) = $this->getPaginationParams();

        if (!$groupName) {
            $this->renderError('Invalid group', 'Group name is required', 400);
        }

        try {
            $result = $this->api->getGroupOverview($groupName, $page, $pageSize);

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Groups', 'url' => '/groups'],
                ['title' => $groupName, 'url' => "/groups/{$groupName}"]
            ]);

            // Go backend returns data in 'data' field, not 'articles'
            $articles = $result['data'] ?? [];
            $pagination = [
                'current_page' => $result['page'] ?? $page,
                'page_size' => $result['page_size'] ?? $pageSize,
                'total_count' => $result['total_count'] ?? 0,
                'total_pages' => $result['total_pages'] ?? 1,
                'has_next' => $result['has_next'] ?? false,
                'has_prev' => $result['has_prev'] ?? false
            ];

            $this->render('group', [
                'title' => $groupName,
                'group_name' => $groupName,
                'articles' => $articles,
                'pagination' => $pagination,
                'breadcrumb' => $breadcrumb,
                'current_page' => $page,
                'page_size' => $pageSize
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load group', $e->getMessage());
        }
    }

    /**
     * Group threads page
     */
    public function threads($params) {
        $groupName = $params['group'] ?? '';
        list($page, $pageSize) = $this->getPaginationParams();

        if (!$groupName) {
            $this->renderError('Invalid group', 'Group name is required', 400);
        }

        try {
            $result = $this->api->getGroupThreads($groupName, $page, $pageSize);

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Groups', 'url' => '/groups'],
                ['title' => $groupName, 'url' => "/groups/{$groupName}"],
                ['title' => 'Threads', 'url' => "/groups/{$groupName}/threads"]
            ]);

            // Go backend returns threads directly (not paginated currently)
            $threads = is_array($result) ? $result : [];
            $pagination = [
                'page' => $page,
                'page_size' => $pageSize,
                'total_count' => count($threads),
                'total_pages' => 1,
                'has_next' => false,
                'has_prev' => false
            ];

            $this->render('threads', [
                'title' => "{$groupName} - Threads",
                'group_name' => $groupName,
                'threads' => $threads,
                'pagination' => $pagination,
                'breadcrumb' => $breadcrumb,
                'current_page' => $page,
                'page_size' => $pageSize
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load threads', $e->getMessage());
        }
    }
}

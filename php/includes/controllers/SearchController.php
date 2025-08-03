<?php
/**
 * Search Controller
 */

require_once __DIR__ . '/BaseController.php';

class SearchController extends BaseController {

    /**
     * Search page
     */
    public function index($params) {
        $query = $_GET['q'] ?? '';
        $group = $_GET['group'] ?? null;
        $page = (int)($_GET['page'] ?? 1);
        $pageSize = min(50, max(1, (int)($_GET['page_size'] ?? 20)));

        $result = null;
        $error = null;

        if ($query) {
            try {
                $result = $this->api->search($query, $group, $page, $pageSize);
            } catch (Exception $e) {
                $error = $e->getMessage();
            }
        }

        $breadcrumb = $this->buildBreadcrumbFromPath([
            ['title' => 'Search', 'url' => '/search']
        ]);

        $this->render('search', [
            'title' => 'Search',
            'query' => $query,
            'group' => $group,
            'result' => $result,
            'error' => $error,
            'breadcrumb' => $breadcrumb,
            'current_page' => $page,
            'page_size' => $pageSize
        ]);
    }
}

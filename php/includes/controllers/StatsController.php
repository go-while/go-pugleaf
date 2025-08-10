<?php
/**
 * Stats Controller
 */

require_once __DIR__ . '/BaseController.php';

class StatsController extends BaseController {

    /**
     * Stats page
     */
    public function index($params) {
        try {
            $stats = $this->api->getStats();

            $breadcrumb = $this->buildBreadcrumbFromPath([
                ['title' => 'Statistics', 'url' => '/stats']
            ]);

            $this->render('stats', [
                'title' => 'Statistics',
                'stats' => $stats,
                'breadcrumb' => $breadcrumb
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load statistics', $e->getMessage());
        }
    }
}

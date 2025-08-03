<?php
/**
 * Home Controller
 */

require_once __DIR__ . '/BaseController.php';

class HomeController extends BaseController {

    /**
     * Home page - sections list
     */
    public function index($params) {
        try {
            // Get real data from working APIs
            $groups = $this->api->getNewsgroups(1, 1); // Just get first page to get totals
            $sections = $this->api->getSections(); // Still empty but needed for template

            // Calculate real stats from groups data
            $realStats = [
                'total_groups' => $groups['total_count'] ?? 0,
                'total_articles' => 0, // We'll calculate this
                'total_pages' => $groups['total_pages'] ?? 0,
                'last_updated' => date('c')
            ];

            // Calculate total articles by summing message_count from groups
            if (isset($groups['data']) && is_array($groups['data'])) {
                foreach ($groups['data'] as $group) {
                    $realStats['total_articles'] += $group['message_count'] ?? 0;
                }
            }

            $this->render('home', [
                'title' => 'Home',
                'sections' => $sections['data'] ?? [],
                'stats' => $realStats,
                'breadcrumb' => []
            ]);

        } catch (Exception $e) {
            $this->renderError('Failed to load home page', $e->getMessage());
        }
    }
}

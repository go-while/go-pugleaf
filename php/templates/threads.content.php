<?php
/**
 * Threads listing template
 */
$template = 'base';
?>

<div class="d-flex justify-content-between align-items-center mb-4">
    <div>
        <h1><?= h($group_name) ?> - Threads</h1>
        <div class="btn-group" role="group">
            <a href="/groups/<?= urlencode($group_name) ?>" class="btn btn-sm btn-outline-primary">
                Articles
            </a>
            <a href="/groups/<?= urlencode($group_name) ?>/threads" class="btn btn-sm btn-outline-primary active">
                Threads
            </a>
        </div>
    </div>
    <div>
        <?php if (!empty($pagination)): ?>
        <small class="text-muted">
            Page <?= $pagination['current_page'] ?? 1 ?> of <?= $pagination['total_pages'] ?? 1 ?>
        </small>
        <?php endif; ?>
    </div>
</div>

<?php if (!empty($threads)): ?>
<div class="alert alert-info">
    <h4>Thread View</h4>
    <p>This shows the thread structure for this newsgroup. Each thread represents a conversation tree.</p>
</div>

<div class="table-responsive">
    <table class="table table-hover">
        <thead>
            <tr>
                <th>Thread ID</th>
                <th>Root Article</th>
                <th>Parent Article</th>
                <th>Child Article</th>
                <th>Depth</th>
                <th>Order</th>
            </tr>
        </thead>
        <tbody>
            <?php foreach ($threads as $thread): ?>
            <tr>
                <td>
                    <span class="badge bg-primary"><?= (int)$thread['id'] ?></span>
                </td>
                <td>
                    <a href="/groups/<?= urlencode($group_name) ?>/articles/<?= (int)$thread['root_article'] ?>"
                       class="text-decoration-none">
                        Article #<?= (int)$thread['root_article'] ?>
                    </a>
                </td>
                <td>
                    <?php if ($thread['parent_article']): ?>
                    <a href="/groups/<?= urlencode($group_name) ?>/articles/<?= (int)$thread['parent_article'] ?>"
                       class="text-decoration-none">
                        Article #<?= (int)$thread['parent_article'] ?>
                    </a>
                    <?php else: ?>
                    <span class="text-muted">Root</span>
                    <?php endif; ?>
                </td>
                <td>
                    <a href="/groups/<?= urlencode($group_name) ?>/articles/<?= (int)$thread['child_article'] ?>"
                       class="text-decoration-none">
                        Article #<?= (int)$thread['child_article'] ?>
                    </a>
                </td>
                <td>
                    <span class="badge bg-secondary"><?= (int)$thread['depth'] ?></span>
                </td>
                <td>
                    <?= (int)$thread['thread_order'] ?>
                </td>
            </tr>
            <?php endforeach; ?>
        </tbody>
    </table>
</div>

<?php if (!empty($pagination) && $pagination['total_pages'] > 1): ?>
<div class="d-flex justify-content-center">
    <?= pagination($pagination['current_page'], $pagination['total_pages'], "/groups/" . urlencode($group_name) . "/threads", ['page_size' => $page_size]) ?>
</div>
<?php endif; ?>

<?php else: ?>
<div class="alert alert-info">
    <h4>No Threads Found</h4>
    <p>This group doesn't have any thread data yet, or threads are still being processed.</p>
    <a href="/groups/<?= urlencode($group_name) ?>" class="btn btn-primary">View Articles</a>
    <a href="/groups" class="btn btn-secondary">Browse Other Groups</a>
</div>
<?php endif; ?>

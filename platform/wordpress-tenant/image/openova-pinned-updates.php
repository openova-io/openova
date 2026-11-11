<?php
/**
 * Plugin Name: OpenOva — versions come from the image
 * Description: WordPress core, plugins and themes are pinned by the bp-wordpress-tenant image; an update is an image bump, never a fetch from api.wordpress.org. Removes the update checks.
 * Version: 1.0.0
 *
 * Why (Refs #7067, hw307 walk): wp_version_check() runs on admin_init and
 * reads information_schema for its database statistics. pg4wp (the Postgres
 * driver) refuses information_schema and throws, so EVERY wp-admin page load
 * ended in "There has been a critical error". The same checks also call
 * api.wordpress.org from a Sovereign that must run without that tether
 * (bp-self-sovereign-cutover step 08 holds a deny-egress window), and an
 * in-place update would diverge from the image every region runs.
 *
 * Must-use: loads before admin_init and cannot be deactivated from wp-admin.
 */

defined( 'ABSPATH' ) || exit;

// The update checks wp-includes/update.php registers at load time.
foreach ( array(
	array( 'admin_init', '_maybe_update_core' ),
	array( 'admin_init', '_maybe_update_plugins' ),
	array( 'admin_init', '_maybe_update_themes' ),
	array( 'wp_version_check', 'wp_version_check' ),
	array( 'wp_update_plugins', 'wp_update_plugins' ),
	array( 'wp_update_themes', 'wp_update_themes' ),
	array( 'load-plugins.php', 'wp_update_plugins' ),
	array( 'load-themes.php', 'wp_update_themes' ),
	array( 'load-update.php', 'wp_update_plugins' ),
	array( 'load-update.php', 'wp_update_themes' ),
	array( 'load-update-core.php', 'wp_update_plugins' ),
	array( 'load-update-core.php', 'wp_update_themes' ),
	array( 'wp_maybe_auto_update', 'wp_maybe_auto_update' ),
) as $hook ) {
	remove_action( $hook[0], $hook[1] );
}

// No automatic updates of anything.
add_filter( 'automatic_updater_disabled', '__return_true' );
add_filter( 'auto_update_core', '__return_false' );
add_filter( 'auto_update_plugin', '__return_false' );
add_filter( 'auto_update_theme', '__return_false' );

// Answer "nothing to update" so the dashboard never tries to fetch.
$openova_no_updates = static function () {
	global $wp_version;
	return (object) array(
		'last_checked'    => time(),
		'version_checked' => $wp_version,
		'updates'         => array(),
		'translations'    => array(),
		'response'        => array(),
		'no_update'       => array(),
		'checked'         => array(),
	);
};
add_filter( 'pre_site_transient_update_core', $openova_no_updates );
add_filter( 'pre_site_transient_update_plugins', $openova_no_updates );
add_filter( 'pre_site_transient_update_themes', $openova_no_updates );

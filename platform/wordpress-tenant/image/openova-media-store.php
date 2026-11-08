<?php
/**
 * Plugin Name: OpenOva media store
 * Description: Stores WordPress media in the Sovereign's replicated SeaweedFS S3 bucket (ADR-0015 D6) via the baked humanmade/S3-Uploads plugin, and serves it back under the site's own /wp-content/uploads/ URLs.
 * Version: 1.0.0
 * Author: OpenOva Catalyst
 *
 * WHY (ADR-0015 D6, Refs #7067): uploads on a region-local PVC stay behind
 * in the old region when the primary moves. bp-seaweedfs >= 1.3.0 replicates
 * every bucket named `replicated[-*]` active-active between the regions with
 * the native `weed filer.sync`, so media written here exist in every region.
 *
 * This is a MUST-USE plugin: it cannot be deactivated from wp-admin and needs
 * no activation click. It does nothing unless the chart set the env contract
 * below (bp-wordpress-tenant templates/deployment.yaml, media.store=s3):
 *
 *   OPENOVA_MEDIA_S3_ENDPOINT   http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333
 *   OPENOVA_MEDIA_S3_BUCKET     replicated
 *   OPENOVA_MEDIA_S3_PREFIX     <org>/<app>           (key prefix, no slashes at the ends)
 *   OPENOVA_MEDIA_S3_REGION     us-east-1             (SigV4 region; SeaweedFS ignores it)
 *   OPENOVA_MEDIA_S3_CREDENTIALS_DIR  /etc/openova/media-s3 — a mounted Secret
 *       volume with files `access-key-id` + `secret-access-key`. Read per
 *       request (not env), so a credential the reflector fills or rotates
 *       after the Pod started is picked up without a restart.
 *
 * WRITE PATH: S3-Uploads' s3:// stream wrapper. S3_UPLOADS_BUCKET is
 * `<bucket>/<prefix>`, so an upload lands at
 * `<bucket>/<prefix>/uploads/YYYY/MM/<file>` (path-style addressing, custom
 * endpoint — the plugin's documented `s3_uploads_s3_client_params` filter).
 *
 * READ PATH: S3_UPLOADS_BUCKET_URL is the site's own WP_CONTENT_URL, so media
 * URLs keep the exact shape they had on the PVC
 * (`https://<host>/wp-content/uploads/...`) — existing post content keeps
 * working, and the browser never needs S3 credentials or a public bucket.
 * Such a URL matches no local file, so WordPress's own .htaccess routes it to
 * index.php; this plugin answers it at `muplugins_loaded` (before themes and
 * regular plugins load) by streaming the object from S3, honouring Range and
 * If-None-Match. A key that does not exist falls through to WordPress's 404.
 */

if ( ! defined( 'ABSPATH' ) ) {
	exit;
}

$openova_media_env = static function ( $name ) {
	$v = getenv( $name );
	return ( false === $v ) ? '' : trim( (string) $v );
};

if ( '' === $openova_media_env( 'OPENOVA_MEDIA_S3_ENDPOINT' ) ) {
	return; // media.store != s3 (or a wp-cli hook Job without the contract).
}

$openova_s3_uploads = '/usr/src/openova-wp-artifacts/plugins/s3-uploads/s3-uploads.php';
if ( ! is_readable( $openova_s3_uploads ) ) {
	// Fail LOUD, never fall back to local disk: a silent fallback writes
	// media into the ephemeral wp-content and loses them on the next restart.
	header( 'HTTP/1.1 503 Service Unavailable' );
	die( 'OpenOva media store: the baked S3-Uploads plugin is missing from the runtime image (platform/wordpress-tenant/image).' );
}

$openova_cred_dir = rtrim( $openova_media_env( 'OPENOVA_MEDIA_S3_CREDENTIALS_DIR' ), '/' );
$openova_cred     = static function ( $file ) use ( $openova_cred_dir ) {
	$p = $openova_cred_dir . '/' . $file;
	return ( '' !== $openova_cred_dir && is_readable( $p ) ) ? trim( (string) file_get_contents( $p ) ) : '';
};
$openova_access_id = $openova_cred( 'access-key-id' );
$openova_access_sk = $openova_cred( 'secret-access-key' );

$openova_bucket = $openova_media_env( 'OPENOVA_MEDIA_S3_BUCKET' );
$openova_prefix = trim( $openova_media_env( 'OPENOVA_MEDIA_S3_PREFIX' ), '/' );

defined( 'S3_UPLOADS_BUCKET' ) || define( 'S3_UPLOADS_BUCKET', '' === $openova_prefix ? $openova_bucket : $openova_bucket . '/' . $openova_prefix );
defined( 'S3_UPLOADS_REGION' ) || define( 'S3_UPLOADS_REGION', $openova_media_env( 'OPENOVA_MEDIA_S3_REGION' ) ?: 'us-east-1' );
defined( 'S3_UPLOADS_KEY' ) || define( 'S3_UPLOADS_KEY', $openova_access_id );
defined( 'S3_UPLOADS_SECRET' ) || define( 'S3_UPLOADS_SECRET', $openova_access_sk );
defined( 'S3_UPLOADS_BUCKET_URL' ) || define( 'S3_UPLOADS_BUCKET_URL', WP_CONTENT_URL );
// The bucket is private; reads go through the read path below.
defined( 'S3_UPLOADS_OBJECT_ACL' ) || define( 'S3_UPLOADS_OBJECT_ACL', 'private' );

add_filter(
	's3_uploads_s3_client_params',
	static function ( $params ) use ( $openova_media_env, $openova_access_id, $openova_access_sk ) {
		$params['endpoint']                = $openova_media_env( 'OPENOVA_MEDIA_S3_ENDPOINT' );
		$params['use_path_style_endpoint'] = true;
		// ALWAYS explicit credentials. With none, the AWS SDK walks its
		// default provider chain (env, ini, then the EC2 metadata endpoint)
		// and hangs on a link-local address that does not exist here. Not yet
		// reflected => `false` (anonymous) => an immediate AccessDenied, which
		// is the honest failure.
		$params['credentials'] = ( '' !== $openova_access_id && '' !== $openova_access_sk )
			? array( 'key' => $openova_access_id, 'secret' => $openova_access_sk )
			: false;
		return $params;
	}
);

// Load the plugin's composer autoloader (AWS SDK + the S3_Uploads\\* classes)
// up front: S3-Uploads itself only requires it inside init() on
// plugins_loaded, which is after the read path below runs and after wp-cli
// resolves the `s3-uploads` command class.
require_once dirname( $openova_s3_uploads ) . '/vendor/autoload.php';
require_once $openova_s3_uploads;

/**
 * Read path — stream /wp-content/uploads/<key> from the bucket.
 */
add_action(
	'muplugins_loaded',
	static function () use ( $openova_bucket, $openova_prefix ) {
		if ( ( defined( 'WP_CLI' ) && WP_CLI ) || ! isset( $_SERVER['REQUEST_URI'] ) ) {
			return;
		}
		$method = isset( $_SERVER['REQUEST_METHOD'] ) ? strtoupper( $_SERVER['REQUEST_METHOD'] ) : 'GET';
		if ( 'GET' !== $method && 'HEAD' !== $method ) {
			return;
		}
		$path    = (string) parse_url( $_SERVER['REQUEST_URI'], PHP_URL_PATH );
		$content = rtrim( (string) parse_url( WP_CONTENT_URL, PHP_URL_PATH ), '/' );
		$base    = $content . '/uploads/';
		if ( 0 !== strpos( $path, $base ) ) {
			return;
		}
		$rel = rawurldecode( substr( $path, strlen( $content ) + 1 ) ); // uploads/...
		if ( '' === $rel || false !== strpos( $rel, '..' ) || false !== strpos( $rel, "\0" ) || '/' === substr( $rel, -1 ) ) {
			return;
		}
		$key  = ( '' === $openova_prefix ? '' : $openova_prefix . '/' ) . $rel;
		$args = array(
			'Bucket' => $openova_bucket,
			'Key'    => $key,
			'@http'  => array( 'stream' => true ),
		);
		if ( ! empty( $_SERVER['HTTP_RANGE'] ) ) {
			$args['Range'] = (string) $_SERVER['HTTP_RANGE'];
		}
		if ( ! empty( $_SERVER['HTTP_IF_NONE_MATCH'] ) ) {
			$args['IfNoneMatch'] = (string) $_SERVER['HTTP_IF_NONE_MATCH'];
		}
		try {
			$s3     = \S3_Uploads\Plugin::get_instance()->s3();
			$result = 'HEAD' === $method ? $s3->headObject( array_diff_key( $args, array( '@http' => 1 ) ) ) : $s3->getObject( $args );
		} catch ( \Aws\S3\Exception\S3Exception $e ) {
			$status = (int) $e->getStatusCode();
			if ( 304 === $status ) {
				status_header( 304 );
				exit;
			}
			if ( 404 === $status || 'NoSuchKey' === $e->getAwsErrorCode() ) {
				return; // WordPress renders its own 404.
			}
			if ( 416 === $status ) {
				status_header( 416 );
				exit;
			}
			error_log( 'openova-media-store: GET ' . $key . ' failed: ' . $e->getMessage() );
			status_header( 502 );
			exit;
		}
		status_header( ! empty( $result['ContentRange'] ) ? 206 : 200 );
		header( 'Content-Type: ' . ( $result['ContentType'] ?: 'application/octet-stream' ) );
		if ( isset( $result['ContentLength'] ) ) {
			header( 'Content-Length: ' . (int) $result['ContentLength'] );
		}
		if ( ! empty( $result['ContentRange'] ) ) {
			header( 'Content-Range: ' . $result['ContentRange'] );
		}
		if ( ! empty( $result['ETag'] ) ) {
			header( 'ETag: ' . $result['ETag'] );
		}
		if ( ! empty( $result['LastModified'] ) ) {
			header( 'Last-Modified: ' . gmdate( 'D, d M Y H:i:s', strtotime( (string) $result['LastModified'] ) ) . ' GMT' );
		}
		header( 'Accept-Ranges: bytes' );
		header( 'Cache-Control: public, max-age=86400' );
		header( 'X-Content-Type-Options: nosniff' );
		if ( 'HEAD' !== $method ) {
			$body = $result['Body'];
			while ( ! $body->eof() ) {
				echo $body->read( 65536 ); // phpcs:ignore WordPress.Security.EscapeOutput
				flush();
			}
		}
		exit;
	},
	0
);

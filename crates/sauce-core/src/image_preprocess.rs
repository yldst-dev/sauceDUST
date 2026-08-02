use std::io::Cursor;

use image::codecs::jpeg::JpegEncoder;
use image::{imageops::FilterType, GenericImageView, ImageReader, Limits};
use sha2::{Digest, Sha256};

use crate::errors::{SauceError, SauceResult};

const MAX_QUERY_DIMENSION: u32 = 512;
const JPEG_QUALITY: u8 = 92;

pub fn sha256_hex(bytes: &[u8]) -> String {
    let digest = Sha256::digest(bytes);
    hex::encode(digest)
}

pub fn prepare_query_image(bytes: &[u8]) -> SauceResult<Vec<u8>> {
    let mut reader = ImageReader::new(Cursor::new(bytes))
        .with_guessed_format()
        .map_err(|err| SauceError::InvalidInput(err.to_string()))?;
    let mut limits = Limits::default();
    limits.max_image_width = Some(12_000);
    limits.max_image_height = Some(12_000);
    limits.max_alloc = Some(256 * 1024 * 1024);
    reader.limits(limits);

    let image = reader.decode()?;
    let (width, height) = image.dimensions();
    let resized = if width > MAX_QUERY_DIMENSION || height > MAX_QUERY_DIMENSION {
        image.resize(
            MAX_QUERY_DIMENSION,
            MAX_QUERY_DIMENSION,
            FilterType::Triangle,
        )
    } else {
        image
    };
    let rgb = resized.to_rgb8();
    let mut output = Vec::new();
    let mut encoder = JpegEncoder::new_with_quality(&mut output, JPEG_QUALITY);
    encoder.encode_image(&rgb).map_err(SauceError::Image)?;

    Ok(output)
}

#[cfg(test)]
mod tests {
    use std::io::Cursor;

    use image::{DynamicImage, ImageBuffer, ImageFormat, Rgb};

    use super::{prepare_query_image, sha256_hex};

    #[test]
    fn sha256_is_stable() {
        assert_eq!(
            sha256_hex(b"query"),
            "a8b771920b8319e47251d1360f5e880bc18e8d329b0f0d003ea3c7e615558947"
        );
    }

    #[test]
    fn resizes_large_query_image() {
        let image = DynamicImage::ImageRgb8(ImageBuffer::from_pixel(1200, 800, Rgb([1, 2, 3])));
        let mut bytes = Cursor::new(Vec::new());
        image.write_to(&mut bytes, ImageFormat::Png).unwrap();

        let prepared = prepare_query_image(&bytes.into_inner()).unwrap();
        let decoded = image::load_from_memory(&prepared).unwrap();

        assert!(decoded.width() <= 512);
        assert!(decoded.height() <= 512);
    }
}

use std::io::Cursor;

use image::codecs::jpeg::JpegEncoder;
use image::{imageops::FilterType, DynamicImage, GenericImageView, ImageReader, Limits, Pixel};

use crate::errors::{SauceError, SauceResult};
use crate::models::Hashes;

const MAX_IMAGE_PIXELS: u64 = 64_000_000;
const MAX_IMAGE_DIMENSION: u32 = 12_000;
const EMBEDDING_IMAGE_DIMENSION: u32 = 512;
const EMBEDDING_JPEG_QUALITY: u8 = 90;

pub struct HashAndEmbeddingImage {
    pub hashes: Hashes,
    pub embedding_bytes: Vec<u8>,
}

pub fn calculate_hashes(bytes: &[u8]) -> SauceResult<Hashes> {
    let image = decode_limited_image(bytes)?;
    calculate_hashes_from_image(&image)
}

pub fn calculate_hashes_and_embedding_image(bytes: &[u8]) -> SauceResult<HashAndEmbeddingImage> {
    let image = decode_limited_image(bytes)?;
    let hashes = calculate_hashes_from_image(&image)?;
    let embedding_bytes = prepare_embedding_image_from_image(image)?;

    Ok(HashAndEmbeddingImage {
        hashes,
        embedding_bytes,
    })
}

fn decode_limited_image(bytes: &[u8]) -> SauceResult<DynamicImage> {
    let mut reader = ImageReader::new(Cursor::new(bytes))
        .with_guessed_format()
        .map_err(|err| SauceError::InvalidInput(err.to_string()))?;
    let mut limits = Limits::default();
    limits.max_image_width = Some(MAX_IMAGE_DIMENSION);
    limits.max_image_height = Some(MAX_IMAGE_DIMENSION);
    limits.max_alloc = Some(256 * 1024 * 1024);
    reader.limits(limits);
    let image = reader.decode()?;
    let (width, height) = image.dimensions();
    let pixels = u64::from(width) * u64::from(height);
    if pixels > MAX_IMAGE_PIXELS {
        return Err(SauceError::InvalidInput(format!(
            "image pixel count is too large: {pixels}"
        )));
    }
    Ok(image)
}

fn calculate_hashes_from_image(image: &DynamicImage) -> SauceResult<Hashes> {
    let (width, height) = image.dimensions();
    let phash = average_hash(image);
    let dhash = difference_hash(image);

    Ok(Hashes {
        phash,
        dhash,
        width,
        height,
    })
}

fn prepare_embedding_image_from_image(image: DynamicImage) -> SauceResult<Vec<u8>> {
    let (width, height) = image.dimensions();
    let resized = if width > EMBEDDING_IMAGE_DIMENSION || height > EMBEDDING_IMAGE_DIMENSION {
        image.resize(
            EMBEDDING_IMAGE_DIMENSION,
            EMBEDDING_IMAGE_DIMENSION,
            FilterType::Triangle,
        )
    } else {
        image
    };
    let rgb = resized.to_rgb8();
    let mut output = Vec::new();
    let mut encoder = JpegEncoder::new_with_quality(&mut output, EMBEDDING_JPEG_QUALITY);
    encoder.encode_image(&rgb).map_err(SauceError::Image)?;
    Ok(output)
}

fn average_hash(image: &DynamicImage) -> String {
    let gray = image.resize_exact(8, 8, FilterType::Triangle).to_luma8();
    let total: u32 = gray.pixels().map(|p| u32::from(p.0[0])).sum();
    let avg = total / 64;
    let mut value = 0u64;

    for pixel in gray.pixels() {
        value <<= 1;
        if u32::from(pixel.0[0]) >= avg {
            value |= 1;
        }
    }

    format!("{value:016x}")
}

fn difference_hash(image: &DynamicImage) -> String {
    let small = image.resize_exact(9, 8, FilterType::Triangle).to_luma8();
    let mut value = 0u64;

    for y in 0..8 {
        for x in 0..8 {
            value <<= 1;
            let left = small.get_pixel(x, y).to_luma().0[0];
            let right = small.get_pixel(x + 1, y).to_luma().0[0];
            if left > right {
                value |= 1;
            }
        }
    }

    format!("{value:016x}")
}

#[cfg(test)]
mod tests {
    use std::io::Cursor;

    use image::{DynamicImage, ImageBuffer, ImageFormat, Rgb};

    use super::{calculate_hashes, calculate_hashes_and_embedding_image};

    #[test]
    fn calculates_hashes_from_image_bytes() {
        let image = DynamicImage::ImageRgb8(ImageBuffer::from_pixel(16, 16, Rgb([120, 80, 40])));
        let mut bytes = Cursor::new(Vec::new());
        image.write_to(&mut bytes, ImageFormat::Png).unwrap();

        let hashes = calculate_hashes(&bytes.into_inner()).unwrap();

        assert_eq!(hashes.width, 16);
        assert_eq!(hashes.height, 16);
        assert_eq!(hashes.phash.len(), 16);
        assert_eq!(hashes.dhash.len(), 16);
    }

    #[test]
    fn rejects_non_image_bytes() {
        assert!(calculate_hashes(b"https://example.invalid/not-an-image.txt").is_err());
    }

    #[test]
    fn prepares_smaller_embedding_image_with_hashes() {
        let image = DynamicImage::ImageRgb8(ImageBuffer::from_pixel(1600, 900, Rgb([120, 80, 40])));
        let mut bytes = Cursor::new(Vec::new());
        image.write_to(&mut bytes, ImageFormat::Png).unwrap();

        let output = calculate_hashes_and_embedding_image(&bytes.into_inner()).unwrap();
        let decoded = image::load_from_memory(&output.embedding_bytes).unwrap();

        assert_eq!(output.hashes.width, 1600);
        assert_eq!(output.hashes.height, 900);
        assert!(decoded.width() <= 512);
        assert!(decoded.height() <= 512);
    }
}

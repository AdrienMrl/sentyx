cmake_minimum_required(VERSION 3.16)
project(teslcam_camera_scorer CXX)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

if(NOT DEFINED NANODET_DIR)
  message(FATAL_ERROR "set NANODET_DIR to a NanoDet checkout")
endif()

find_package(OpenCV REQUIRED)
find_package(ncnn REQUIRED)
find_package(OpenMP REQUIRED)

# NanoDet's 2021 demo sets thread count on Extractor, an API removed by modern
# NCNN. Thread count now lives on Net::opt (set by main.cpp), so generate a
# compatibility copy without that obsolete call instead of modifying the
# upstream checkout.
file(READ ${NANODET_DIR}/demo_ncnn/nanodet.cpp NANODET_SOURCE)
string(REPLACE "ex.set_num_threads(4);" "" NANODET_SOURCE "${NANODET_SOURCE}")
file(WRITE ${CMAKE_CURRENT_BINARY_DIR}/nanodet_compat.cpp "${NANODET_SOURCE}")

add_executable(teslcam-camera-scorer main.cpp ${CMAKE_CURRENT_BINARY_DIR}/nanodet_compat.cpp)
target_include_directories(teslcam-camera-scorer PRIVATE
  ${NANODET_DIR}/demo_ncnn
  ${OpenCV_INCLUDE_DIRS}
)
target_link_libraries(teslcam-camera-scorer PRIVATE ncnn ${OpenCV_LIBS} OpenMP::OpenMP_CXX)
